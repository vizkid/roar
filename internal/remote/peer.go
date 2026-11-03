package remote

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vizkid/rexkit/rexctl"
	"github.com/vizkid/roar/internal/config"
	"github.com/vizkid/roar/internal/roard"
)

var (
	ErrNoRoard        = errors.New("roard_not_running")
	ErrRoardAmbiguous = errors.New("roard_ambiguous")
)

// Stats counts what one Peer sent, for tests and the live report.
type Stats struct {
	Calls     map[string]int
	PartBytes int64 // raw attachment bytes roard acknowledged
	SentBytes int64 // raw attachment bytes sent, counting parts lost to a drop
}

func (s Stats) Total() int {
	n := 0
	for _, c := range s.Calls {
		n += c
	}
	return n
}

// Peer talks to the roard on another host's Rex server, over one control
// connection that also carries roard's roar.result calls back. Every call's
// deadline follows the measured round trip; every retry backs off.
type Peer struct {
	Endpoint string // the host's Rex server
	Label    string // roard's client label; "roard" when empty
	Settings *config.Settings
	Stats    Stats

	afterPart func(*rexctl.Client, int) // test hook
	dialFails int
	mu        sync.Mutex
	rtt       config.RTT
}

// SeedRTT starts the smoothed round trip from an earlier command's, so the
// first deadlines already fit a slow link.
func (p *Peer) SeedRTT(d time.Duration) {
	if d > 0 {
		p.rtt.Alpha = p.Settings.Float("sender.call.rtt_alpha")
		p.rtt.Observe(d)
	}
}

// SRTT is the smoothed round trip measured so far.
func (p *Peer) SRTT() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rtt.SRTT()
}

// link is one open connection to roard.
type link struct {
	c       *rexctl.Client
	roard   string
	maxPart int
	results chan roard.Result
}

func (p *Peer) label() string {
	if p.Label == "" {
		return "roard"
	}
	return p.Label
}

// dial connects, offers roar.result, finds roard, and says hello.
func (p *Peer) dial(ctx context.Context) (*link, error) {
	s := p.Settings
	if p.rtt.Alpha == 0 {
		p.rtt.Alpha = s.Float("sender.call.rtt_alpha")
	}
	results := make(chan roard.Result, 256)
	// Opening takes two round trips: the WebSocket handshake, then hello and
	// welcome. The deadline follows the last known round trip, so a 20 s
	// link gets time to open, and the open itself gives the first sample.
	open := rexctl.Options{Endpoint: p.Endpoint, Info: rexctl.Info{Kind: "cli", Name: "roar"},
		OnPerform: func(pf rexctl.Perform) (any, *rexctl.Error) {
			var r roard.Result
			if pf.Action != "roar.result" || json.Unmarshal(pf.Args, &r) != nil {
				return nil, &rexctl.Error{Code: "not_supported", Message: pf.Action}
			}
			select {
			case results <- r:
			default: // the store is authoritative; a resubscribe reads it
			}
			return "ok", nil
		}}
	// A dial that times out teaches nothing about the link, so each failed
	// try waits longer, by the backoff factor: 10 s, then 20 s, then 40 s.
	floor := s.Duration("sender.connect.timeout")
	for range p.dialFails {
		floor = time.Duration(float64(floor) * s.Float("sender.connect.factor"))
	}
	connect := config.Deadline{Timeout: floor, RTTFactor: s.Float("sender.call.rtt_factor")}
	dctx, cancel := context.WithTimeout(ctx, connect.For(p.SRTT()))
	t0 := time.Now()
	c, err := rexctl.Dial(dctx, open)
	cancel()
	if err != nil {
		p.dialFails++
		return nil, err
	}
	p.dialFails = 0
	p.mu.Lock()
	if p.rtt.SRTT() == 0 {
		p.rtt.Observe(time.Since(t0) / 2) // the handshake, then hello and welcome
	}
	p.mu.Unlock()
	// The part size follows from the server's message limit, which the
	// welcome already gave: no hello needed. Offering roar.result and finding
	// roard go out together, one round trip on a slow link instead of two.
	l := &link{c: c, results: results, maxPart: c.MaxMessage() * 3 / 4}
	cctx, cancel := context.WithTimeout(ctx, s.Deadline("sender.call").For(p.SRTT()))
	defer cancel()
	declared := make(chan error, 1)
	go func() { declared <- c.Declare(cctx, "", []rexctl.Action{{Name: "roar.result", Title: "Roar: result"}}) }()
	l.roard, err = c.Resolve(cctx, p.label())
	if derr := <-declared; err == nil {
		err = derr
	}
	switch {
	case errors.Is(err, rexctl.ErrNoClient):
		err = ErrNoRoard
	case errors.Is(err, rexctl.ErrAmbiguous):
		err = ErrRoardAmbiguous
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return l, nil
}

// call runs one action with a deadline from the smoothed round trip.
func (p *Peer) call(ctx context.Context, l *link, action string, args, out any) error {
	s := p.Settings
	cctx, cancel := context.WithTimeout(ctx, s.Deadline("sender.call").For(p.SRTT()))
	defer cancel()
	p.mu.Lock()
	if p.Stats.Calls == nil {
		p.Stats.Calls = map[string]int{}
	}
	p.Stats.Calls[action]++
	p.mu.Unlock()
	t0 := time.Now()
	err := l.c.Perform(cctx, l.roard, action, args, s.Duration("sender.call.remote_timeout"), out)
	if err == nil {
		p.mu.Lock()
		p.rtt.Observe(time.Since(t0))
		p.mu.Unlock()
	}
	return err
}

// start sends one call without waiting, for parts kept in flight. finish
// waits for it with the same latency-scaled deadline as call.
func (p *Peer) start(l *link, action string, args any) (func(context.Context, any) error, error) {
	p.mu.Lock()
	if p.Stats.Calls == nil {
		p.Stats.Calls = map[string]int{}
	}
	p.Stats.Calls[action]++
	p.mu.Unlock()
	t0 := time.Now()
	wait, err := l.c.StartPerform(l.roard, action, args, p.Settings.Duration("sender.call.remote_timeout"))
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, out any) error {
		err := wait(ctx, out)
		if err == nil {
			p.mu.Lock()
			p.rtt.Observe(time.Since(t0))
			p.mu.Unlock()
		}
		return err
	}, nil
}

func (p *Peer) finish(ctx context.Context, wait func(context.Context, any) error, out any) error {
	cctx, cancel := context.WithTimeout(ctx, p.Settings.Deadline("sender.call").For(p.SRTT()))
	defer cancel()
	return wait(cctx, out)
}

// hints tell roard how patient to be when calling this sender back.
func (p *Peer) hints() roard.Hints {
	s := p.Settings
	return roard.Hints{
		CallbackTimeoutMS: s.Deadline("sender.call").For(p.SRTT()).Milliseconds(),
		CallbackGiveUpMS:  (s.Duration("sender.print_deadline") + s.Duration("sender.transfer.base")).Milliseconds(),
	}
}

// settled is a state worth printing: delivered or failed, or held after the
// first delivery pass gave a reason.
func settled(r roard.Row) bool {
	return r.State != "missing" && (r.State != "queued" || r.HoldReason != "")
}

// fatal reports an error no retry can fix: a refusal, or a message too big.
func fatal(err error) bool {
	return rexctl.Code(err) == "refused" || rexctl.Code(err) == "bad_request" || errors.Is(err, rexctl.ErrTooLarge) ||
		errors.Is(err, ErrNoRoard) || errors.Is(err, ErrRoardAmbiguous)
}

func refusal(err error) error {
	var re *rexctl.Error
	if errors.As(err, &re) {
		return fmt.Errorf("%w: %s", ErrRefused, re.Message)
	}
	return err
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func encodedSize(files map[string][]byte) int64 {
	var n int64
	for _, b := range files {
		n += int64(base64.StdEncoding.EncodedLen(len(b)))
	}
	return n
}
