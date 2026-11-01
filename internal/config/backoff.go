package config

import (
	"math"
	"math/rand/v2"
	"time"
)

// Backoff is the one shape every Roar retry loop uses.
type Backoff struct {
	Initial    time.Duration
	Factor     float64
	Max        time.Duration
	Jitter     float64       // 0 to 1: the random share taken off each delay
	ResetAfter time.Duration // 0: never reset
}

// Delay is the delay before try n+1, counting from 0. u is a random number
// in [0, 1): the delay is picked between delay×(1−jitter) and delay, so
// clients that fail together do not retry together.
func (b Backoff) Delay(n int, u float64) time.Duration {
	d := float64(b.Initial) * math.Pow(b.Factor, float64(n))
	if d > float64(b.Max) || math.IsInf(d, 0) || math.IsNaN(d) {
		d = float64(b.Max)
	}
	return time.Duration(d - d*b.Jitter*u)
}

// Retry counts failures for one loop. Up marks a connection or call that
// works; one that lasts ResetAfter makes the next failure start over.
type Retry struct {
	b    Backoff
	rnd  func() float64
	n    int
	up   time.Time
	isUp bool
}

// NewRetry starts a loop. rnd is nil for a real random source.
func NewRetry(b Backoff, rnd func() float64) *Retry {
	if rnd == nil {
		rnd = rand.Float64
	}
	return &Retry{b: b, rnd: rnd}
}

// Fail records a failure at now and returns how long to wait.
func (r *Retry) Fail(now time.Time) time.Duration {
	if r.isUp && r.b.ResetAfter > 0 && now.Sub(r.up) >= r.b.ResetAfter {
		r.n = 0
	}
	r.isUp = false
	d := r.b.Delay(r.n, r.rnd())
	r.n++
	return d
}

// Up records that the connection or call worked at now.
func (r *Retry) Up(now time.Time) { r.up, r.isUp = now, true }

// SetBackoff changes the shape, as a config reload does, keeping the count.
func (r *Retry) SetBackoff(b Backoff) { r.b = b }

// Deadline is how long one call may take: the larger of a floor and a
// multiple of the smoothed round trip. A spike of 20s gives 160s with the
// default factor of 8. It is the caller's own wait, so Rex's 30s cap on
// timeout_ms does not apply; callers bound it by their whole budget.
type Deadline struct {
	Timeout   time.Duration
	RTTFactor float64
}

func (d Deadline) For(srtt time.Duration) time.Duration {
	return max(d.Timeout, time.Duration(d.RTTFactor*float64(srtt)))
}

// RTT is a smoothed round trip: srtt = (1−α)×srtt + α×sample, starting
// at the first sample.
type RTT struct {
	Alpha float64
	srtt  time.Duration
	seen  bool
}

func (r *RTT) Observe(sample time.Duration) {
	if !r.seen {
		r.srtt, r.seen = sample, true
		return
	}
	r.srtt = time.Duration((1-r.Alpha)*float64(r.srtt) + r.Alpha*float64(sample))
}

// SRTT is the smoothed round trip, 0 before any sample.
func (r *RTT) SRTT() time.Duration { return r.srtt }
