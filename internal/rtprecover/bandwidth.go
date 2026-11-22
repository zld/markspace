package rtprecover

import (
	"math"
	"time"

	"github.com/archive/mediakernel/internal/timeline"
)

// Estimator maintains an exponentially weighted moving average of the observed
// delivery bandwidth. The estimate evolves as the link changes and feeds the
// segment output-budget planner, which must keep planned bitrate at or below
// the available bandwidth while tracking the configured target.
type Estimator struct {
	alpha       float64 // smoothing factor in (0,1]
	initialized bool
	ewmaBps     float64 // bits per second
	minBps      float64 // floor so a collapsed link cannot drive budget to zero
	samples     int
}

// NewEstimator creates an EWMA estimator. alpha is the weight of each new
// sample (typical 0.125..0.5); minBps is the floor applied to every estimate.
func NewEstimator(alpha, minBps float64) (*Estimator, error) {
	if alpha <= 0 || alpha > 1 {
		return nil, timeline.NewError(timeline.KindInvalidInput, "ewma alpha must be in (0,1], got %v", alpha)
	}
	if minBps <= 0 {
		return nil, timeline.NewError(timeline.KindInvalidInput, "min bandwidth must be positive")
	}
	return &Estimator{alpha: alpha, minBps: minBps}, nil
}

// Observe records one delivered payload: payloadBytes received over the given
// wall-clock interval. Zero-length intervals are rejected.
func (e *Estimator) Observe(payloadBytes int, interval time.Duration) error {
	if payloadBytes < 0 {
		return timeline.NewError(timeline.KindInvalidInput, "negative payload bytes")
	}
	if interval <= 0 {
		return timeline.NewError(timeline.KindInvalidInput, "bandwidth sample interval must be positive")
	}
	bps := float64(payloadBytes*8) / interval.Seconds()
	if !e.initialized {
		e.ewmaBps = bps
		e.initialized = true
	} else {
		e.ewmaBps = e.alpha*bps + (1-e.alpha)*e.ewmaBps
	}
	if e.ewmaBps < e.minBps {
		e.ewmaBps = e.minBps
	}
	e.samples++
	return nil
}

// Bps returns the current bandwidth estimate in bits per second. Before any
// sample the configured floor is returned so planning stays deterministic.
func (e *Estimator) Bps() float64 {
	if !e.initialized {
		return e.minBps
	}
	return e.ewmaBps
}

// Samples returns the number of incorporated observations.
func (e *Estimator) Samples() int { return e.samples }

// Budget is one segment's planned output budget.
type Budget struct {
	Index             int
	DurationUS        int64
	PlannedBitrateBps int64
	PlannedBytes      int64
}

// PlanBudgets divides the presented duration into segments of segDuration and
// assigns each a planned bitrate: the configured target, capped to the current
// bandwidth estimate. Because the estimate is re-read for every segment, a
// changing link reshapes all subsequent budgets while earlier segments keep
// theirs (a finished segment is never replanned).
func PlanBudgets(totalDurationUS int64, segDurationUS int64, targetBps int64, est *Estimator) ([]Budget, error) {
	if totalDurationUS <= 0 || segDurationUS <= 0 {
		return nil, timeline.NewError(timeline.KindInvalidInput, "durations must be positive")
	}
	if targetBps <= 0 || est == nil {
		return nil, timeline.NewError(timeline.KindInvalidInput, "target bitrate and estimator required")
	}
	n := int((totalDurationUS + segDurationUS - 1) / segDurationUS)
	out := make([]Budget, 0, n)
	for i := 0; i < n; i++ {
		start := int64(i) * segDurationUS
		dur := segDurationUS
		if remaining := totalDurationUS - start; remaining < dur {
			dur = remaining
		}
		rate := float64(targetBps)
		if avail := est.Bps(); avail < rate {
			rate = avail // never plan beyond what the link currently delivers
		}
		plannedBps := int64(rate)
		// bytes = bitrate * seconds / 8 = bps * dur_us / 8e6. Float64 avoids an
		// int64 overflow for the bps*microsecond product on long material; the
		// relative precision loss is negligible for byte budgets.
		bytesF := math.Round(float64(plannedBps) * float64(dur) / 8_000_000)
		if bytesF < 0 || bytesF > float64(math.MaxInt64) {
			return nil, timeline.NewError(timeline.KindLimit, "segment byte budget out of range")
		}
		bytes := int64(bytesF)
		out = append(out, Budget{
			Index: i, DurationUS: dur,
			PlannedBitrateBps: plannedBps, PlannedBytes: bytes,
		})
	}
	return out, nil
}

// BitrateError returns the signed percentage deviation of actual from target:
// (actual-target)/target*100. The result is always finite for positive target.
func BitrateError(targetBps, actualBps int64) (float64, error) {
	if targetBps <= 0 {
		return 0, timeline.NewError(timeline.KindInvalidInput, "target bitrate must be positive")
	}
	return float64(actualBps-targetBps) / float64(targetBps) * 100, nil
}
