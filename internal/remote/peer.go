package remote

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/vizkid/roar/internal/config"
	"github.com/vizkid/roar/internal/rexctl"
	"github.com/vizkid/roar/internal/roard"
)

var (
	ErrNoRoard        = errors.New("roard_not_running")
	ErrRoardAmbiguous = errors.New("roard_ambiguous")
)

// Stats counts what one Peer sent, for tests and the live report.
type Stats struct {
	Calls     map[string]int
	PartBytes int64 // raw attachment bytes sent in parts
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
	rtt       config.RTT
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
	dctx, cancel := context.WithTimeout(ctx, s.Duration("sender.connect.timeout"))
	defer cancel()
	c, err := rexctl.Dial(dctx, rexctl.Options{Endpoint: p.Endpoint, Info: rexctl.Info{Kind: "cli", Name: "roar"},
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
		}})
	if err != nil {
		return nil, err
	}
	l := &link{c: c, results: results}
	err = c.Declare(dctx, "", []rexctl.Action{{Name: "roar.result", Title: "Roar: result"}})
	if err == nil {
		l.roard, err = c.Resolve(dctx, p.label())
	}
	switch {
	case errors.Is(err, rexctl.ErrNoClient):
		err = ErrNoRoard
	case errors.Is(err, rexctl.ErrAmbiguous):
		err = ErrRoardAmbiguous
	}
	if err == nil {
		var hello struct {
			MaxPart int `json:"max_part"`
		}
		err = p.call(ctx, l, "roar.hello", nil, &hello)
		l.maxPart = hello.MaxPart
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
	cctx, cancel := context.WithTimeout(ctx, s.Deadline("sender.call").For(p.rtt.SRTT()))
	defer cancel()
	if p.Stats.Calls == nil {
		p.Stats.Calls = map[string]int{}
	}
	p.Stats.Calls[action]++
	t0 := time.Now()
	err := l.c.Perform(cctx, l.roard, action, args, s.Duration("sender.call.remote_timeout"), out)
	if err == nil {
		p.rtt.Observe(time.Since(t0))
	}
	return err
}

// hints tell roard how patient to be when calling this sender back.
func (p *Peer) hints() roard.Hints {
	s := p.Settings
	return roard.Hints{
		CallbackTimeoutMS: s.Deadline("sender.call").For(p.rtt.SRTT()).Milliseconds(),
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
