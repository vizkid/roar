package remote

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/vizkid/rexkit/rexctl"
	"github.com/vizkid/roar/internal/config"
	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/roard"
)

// sendState carries one send across reconnects.
type sendState struct {
	e         envelope.Envelope
	raw       []byte
	files     map[string][]byte
	committed bool // an enqueue or commit was sent, so the message may exist
	last      roard.Row
	landed    int64 // attachment bytes roard reported committed, for progress

	mu       sync.Mutex // deadline is read by budget's timer while parts move it
	deadline time.Time
}

// until is the time the send must finish by, as progress has moved it.
func (st *sendState) until() time.Time {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.deadline
}

// extend moves the deadline to at least d from now: a moving upload never
// runs out of time.
func (st *sendState) extend(d time.Duration) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if t := time.Now().Add(d); t.After(st.deadline) {
		st.deadline = t
	}
}

// ErrBudget is a send that ran past its transfer budget: sender.transfer.base
// plus the time its size takes at sender.transfer.min_rate.
var ErrBudget = errors.New("transfer_budget: the send ran past its time budget (sender.transfer.base, sender.transfer.min_rate)")

// budget is ctx ending once st's deadline passes. Each dial and call of a
// try runs under it, so one stalled step cannot outlast the whole send. The
// deadline moves while an upload makes progress, so the timer re-reads it.
func budget(ctx context.Context, st *sendState) (context.Context, context.CancelFunc) {
	bctx, cancel := context.WithCancelCause(ctx)
	go func() {
		for {
			t := time.NewTimer(time.Until(st.until()))
			select {
			case <-t.C:
				if !time.Now().Before(st.until()) {
					cancel(ErrBudget)
					return
				}
			case <-bctx.Done():
				t.Stop()
				return
			}
		}
	}()
	return bctx, func() { cancel(nil) }
}

// Send hands e to roard and returns the first settled record, or the latest
// one once print_deadline passes. A lost connection reconnects with backoff
// and resumes; once an enqueue or commit may have run, a send that cannot
// finish returns ErrUnknown, never a refusal.
func (p *Peer) Send(ctx context.Context, e envelope.Envelope, files map[string][]byte) (roard.Row, error) {
	raw, err := json.Marshal(e)
	if err != nil {
		return roard.Row{}, err
	}
	s := p.Settings
	rate := max(s.Size("sender.transfer.min_rate"), 1)
	st := &sendState{e: e, raw: raw, files: files,
		deadline: time.Now().Add(s.Duration("sender.transfer.base") + time.Duration(encodedSize(files)/rate)*time.Second)}
	retry := config.NewRetry(s.Backoff("sender.connect"), nil)
	for {
		row, err := p.sendOnce(ctx, st)
		if err == nil {
			return row, nil
		}
		if fatal(err) && !(st.committed && (errors.Is(err, ErrNoRoard) || errors.Is(err, ErrRoardAmbiguous))) {
			return roard.Row{}, refusal(err)
		}
		wait := retry.Fail(time.Now())
		if errors.Is(err, context.Canceled) && ctx.Err() == nil {
			err = ErrBudget // the try's budget context ended, not the caller's
		}
		if ctx.Err() != nil || time.Now().Add(wait).After(st.until()) {
			if st.committed {
				return st.last, errors.Join(ErrUnknown, err)
			}
			return roard.Row{}, err
		}
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
		}
	}
}

// sendOnce is one try. Dialing and handing over run under the send's
// budget; waiting for the result runs under print_deadline instead, since
// the message may already be in roard's hands.
func (p *Peer) sendOnce(ctx context.Context, st *sendState) (roard.Row, error) {
	bctx, cancel := budget(ctx, st)
	defer cancel()
	l, err := p.dial(bctx)
	if err != nil {
		return roard.Row{}, err
	}
	defer l.c.Close()
	id := st.e.ID
	var sub struct {
		Rows []roard.Row `json:"rows"`
	}
	if err := p.call(bctx, l, "roar.subscribe", map[string]any{"ids": []string{id}, "hints": p.hints()}, &sub); err != nil {
		return roard.Row{}, err
	}
	if len(sub.Rows) == 1 && sub.Rows[0].State != "missing" {
		st.committed, st.last = true, sub.Rows[0] // a try before this one landed
		if settled(st.last) {
			return st.last, nil
		}
	} else if err := p.handOver(bctx, l, st); err != nil {
		return roard.Row{}, err
	}
	return p.await(ctx, l, st)
}

// await waits for roar.result calls until the record settles or
// print_deadline passes. A quiet link is probed by resubscribing.
func (p *Peer) await(ctx context.Context, l *link, st *sendState) (roard.Row, error) {
	s := p.Settings
	printBy := time.NewTimer(s.Duration("sender.print_deadline"))
	defer printBy.Stop()
	stall := time.NewTimer(s.Duration("sender.stall_timeout"))
	defer stall.Stop()
	for {
		select {
		case r := <-l.results:
			if r.Error != nil {
				return roard.Row{}, &rexctl.Error{Code: r.Error.Code, Message: r.Error.Message}
			}
			for _, row := range r.Rows {
				if row.ID == st.e.ID && row.Revision >= st.last.Revision {
					st.last = row
				}
			}
			if settled(st.last) {
				return st.last, nil
			}
		case <-printBy.C:
			if st.last.State == "" {
				st.last = roard.Row{ID: st.e.ID, State: "queued"}
			}
			return st.last, nil
		case <-stall.C:
			var sub struct {
				Rows []roard.Row `json:"rows"`
			}
			if err := p.call(ctx, l, "roar.subscribe", map[string]any{"ids": []string{st.e.ID}, "hints": p.hints()}, &sub); err != nil {
				return roard.Row{}, err
			}
			if len(sub.Rows) == 1 && sub.Rows[0].State != "missing" {
				st.last = sub.Rows[0]
				if settled(st.last) {
					return st.last, nil
				}
			}
			stall.Reset(s.Duration("sender.stall_timeout"))
		case <-l.c.Done():
			return roard.Row{}, l.c.Err()
		case <-ctx.Done():
			return roard.Row{}, ctx.Err()
		}
	}
}

// Status reads records of messages the host holds.
func (p *Peer) Status(ctx context.Context, ids ...string) ([]roard.Row, error) {
	r, err := p.job(ctx, "roar.status", map[string]any{"ids": ids})
	return r.Rows, err
}

// Panes lists the host's panes, as `roar panes --json` prints them.
func (p *Peer) Panes(ctx context.Context) (json.RawMessage, error) {
	r, err := p.job(ctx, "roar.panes", nil)
	if err != nil {
		return nil, err
	}
	return json.Marshal(r.Panes)
}

// job runs one action whose result comes back as a roar.result call.
func (p *Peer) job(ctx context.Context, action string, args any) (roard.Result, error) {
	l, err := p.dial(ctx)
	if err != nil {
		return roard.Result{}, err
	}
	defer l.c.Close()
	var j struct {
		Job string `json:"job"`
	}
	if err := p.call(ctx, l, action, args, &j); err != nil {
		return roard.Result{}, refusal(err)
	}
	wait := time.NewTimer(p.Settings.Deadline("sender.call").For(p.SRTT()) + p.Settings.Duration("sender.print_deadline"))
	defer wait.Stop()
	for {
		select {
		case r := <-l.results:
			if r.Job != j.Job {
				continue
			}
			if r.Error != nil {
				return r, refusal(&rexctl.Error{Code: r.Error.Code, Message: r.Error.Message})
			}
			return r, nil
		case <-wait.C:
			return roard.Result{}, errors.Join(ErrUnknown, errors.New(action+": no result in time"))
		case <-l.c.Done():
			return roard.Result{}, l.c.Err()
		case <-ctx.Done():
			return roard.Result{}, ctx.Err()
		}
	}
}
