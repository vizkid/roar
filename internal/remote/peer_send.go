package remote

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/vizkid/roar/internal/config"
	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/rexctl"
	"github.com/vizkid/roar/internal/roard"
)

// sendState carries one send across reconnects.
type sendState struct {
	e         envelope.Envelope
	raw       []byte
	files     map[string][]byte
	committed bool // an enqueue or commit was sent, so the message may exist
	last      roard.Row
	deadline  time.Time
	landed    int64 // attachment bytes roard reported committed, for progress
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
		if ctx.Err() != nil || time.Now().Add(wait).After(st.deadline) {
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

func (p *Peer) sendOnce(ctx context.Context, st *sendState) (roard.Row, error) {
	l, err := p.dial(ctx)
	if err != nil {
		return roard.Row{}, err
	}
	defer l.c.Close()
	id := st.e.ID
	var sub struct {
		Rows []roard.Row `json:"rows"`
	}
	if err := p.call(ctx, l, "roar.subscribe", map[string]any{"ids": []string{id}, "hints": p.hints()}, &sub); err != nil {
		return roard.Row{}, err
	}
	if len(sub.Rows) == 1 && sub.Rows[0].State != "missing" {
		st.committed, st.last = true, sub.Rows[0] // a try before this one landed
		if settled(st.last) {
			return st.last, nil
		}
	} else if err := p.handOver(ctx, l, st); err != nil {
		return roard.Row{}, err
	}
	return p.await(ctx, l, st)
}

// handOver enqueues inline when everything fits one part, else uploads.
func (p *Peer) handOver(ctx context.Context, l *link, st *sendState) error {
	args := map[string]any{"envelope": string(st.raw), "sha256": digest(st.raw)}
	if int64(len(st.raw))+encodedSize(st.files)+4096 <= int64(l.maxPart) {
		inline := map[string]string{}
		for name, b := range st.files {
			inline[name] = base64.StdEncoding.EncodeToString(b)
		}
		args["files"] = inline
		st.committed = true
		return p.call(ctx, l, "roar.enqueue", args, nil)
	}
	if err := p.upload(ctx, l, st, args); err != nil {
		return err
	}
	st.committed = true
	return p.call(ctx, l, "roar.upload.commit", map[string]any{"upload": "up-" + st.e.ID}, nil)
}

// upload sends each file from its committed offset, keeping in_flight
// parts outstanding. They go out in order on one connection, and Rex hands
// them to roard in that order, so each lands at the offset it names.
func (p *Peer) upload(ctx context.Context, l *link, st *sendState, begin map[string]any) error {
	var b struct {
		Upload    string           `json:"upload"`
		Committed map[string]int64 `json:"committed"`
	}
	if err := p.call(ctx, l, "roar.upload.begin", begin, &b); err != nil {
		return err
	}
	s := p.Settings
	// Parts can land while their replies are lost to a drop. begin shows
	// them, and that progress extends the deadline like an acknowledgement.
	var landed int64
	for _, c := range b.Committed {
		landed += c
	}
	if landed > st.landed {
		st.landed = landed
		st.deadline = maxTime(st.deadline, time.Now().Add(s.Duration("sender.transfer.base")))
	}
	// part_size is base64 bytes; capped so the request stays under the limit.
	raw := int64(min(s.Size("sender.upload.part_size"), int64(l.maxPart))) * 3 / 4
	window := max(1, s.Int("sender.upload.in_flight"))
	type sent struct {
		off  int64
		wait func(context.Context, any) error
	}
	n := 0
	for _, f := range st.e.Files {
		data := st.files[f.Name]
		next := b.Committed[f.Name]
		var pending []sent
		for next < int64(len(data)) || len(pending) > 0 {
			for len(pending) < window && next < int64(len(data)) {
				end := min(next+raw, int64(len(data)))
				wait, err := p.start(l, "roar.upload.part", map[string]any{"upload": b.Upload, "file": f.Name, "offset": next,
					"data": base64.StdEncoding.EncodeToString(data[next:end])})
				if err != nil {
					return err
				}
				pending = append(pending, sent{next, wait})
				p.mu.Lock()
				p.Stats.SentBytes += end - next
				p.mu.Unlock()
				next = end
			}
			head := pending[0]
			pending = pending[1:]
			var r struct {
				Committed int64 `json:"committed"`
			}
			if err := p.finish(ctx, head.wait, &r); err != nil {
				for _, rest := range pending {
					p.finish(ctx, rest.wait, nil) // drain; begin reports what landed
				}
				return err
			}
			p.mu.Lock()
			p.Stats.PartBytes += max(0, r.Committed-head.off)
			p.mu.Unlock()
			// A moving upload never runs out of time.
			st.deadline = maxTime(st.deadline, time.Now().Add(s.Duration("sender.transfer.base")))
			n++
			if p.afterPart != nil {
				p.afterPart(l.c, n)
			}
		}
	}
	return nil
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
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
