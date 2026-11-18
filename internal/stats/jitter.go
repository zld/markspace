// Package stats computes RFC 3550 reception metrics and renders the final
// JSON experiment report.
package stats

import (
	"sync"

	"relaylab/internal/forward"
)

// JitterTracker implements the RFC 3550 A.8 interarrival jitter estimator.
// It is safe for concurrent use; the estimate is kept in RTP clock ticks.
type JitterTracker struct {
	mu        sync.Mutex
	clockRate uint32
	jitter    float64
	lastTS    uint32
	lastArr   uint32 // arrival time expressed in RTP clock units
	started   bool
	received  int64
}

// NewJitterTracker builds a tracker for the given RTP clock rate (Hz).
func NewJitterTracker(clockRate uint32) *JitterTracker {
	if clockRate == 0 {
		clockRate = forward.DefaultClockRate
	}
	return &JitterTracker{clockRate: clockRate}
}

// Observe records one received packet using its RTP timestamp and the local
// arrival time. Arrival is supplied in RTP ticks so tests are deterministic
// and no wall clock is baked into the estimator.
func (j *JitterTracker) Observe(rtpTS uint32, arrivalRTPTicks uint32) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.received++
	if !j.started {
		j.started = true
		j.lastTS = rtpTS
		j.lastArr = arrivalRTPTicks
		return
	}
	// Both deltas are signed and wraparound-aware, so late or wrapped
	// packets never produce giant bogus jitter spikes.
	d := forward.TSDelta(j.lastTS, rtpTS) - int64(forward.TSDelta(j.lastArr, arrivalRTPTicks))
	if d < 0 {
		d = -d
	}
	j.jitter += (float64(d) - j.jitter) / 16.0
	j.lastTS = rtpTS
	j.lastArr = arrivalRTPTicks
}

// JitterTicks returns the smoothed jitter rounded to whole RTP clock ticks.
// Zero packets and a single packet both yield 0 per RFC 3550.
func (j *JitterTracker) JitterTicks() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.jitter < 0 {
		return 0
	}
	return uint64(j.jitter + 0.5)
}

// JitterMS converts the tick estimate to milliseconds using the clock rate.
func (j *JitterTracker) JitterMS() float64 {
	ticks := j.JitterTicks()
	return float64(ticks) * 1000.0 / float64(j.clockRate)
}

// Received reports how many packets fed the estimator.
func (j *JitterTracker) Received() int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.received
}
