package config

import (
	"time"

	"github.com/vizkid/rexkit/backoff"
)

// Backoff, Retry and NewRetry live in rexkit, which roar's Rex client shares
// with Burns.
type (
	Backoff = backoff.Backoff
	Retry   = backoff.Retry
)

// NewRetry starts a retry loop with b.
var NewRetry = backoff.NewRetry

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
