// Package budget selects the highest deliverable PCM output bit-depth using
// 80% of a sliding bandwidth estimate.
package budget

import (
	"time"

	"mediacheck/internal/timebase"
)

const Utilization = 0.80

// Sample is one bandwidth observation at a monotonic logical time.
type Sample struct {
	At  time.Duration
	BPS float64
}

// Window averages bandwidth observations no older than Width before now.
type Window struct {
	Width   time.Duration
	samples []Sample
}

func NewWindow(width time.Duration) (*Window, error) {
	if width <= 0 {
		return nil, timebase.NewError(timebase.CodeInvalidArgument, "bandwidth window must be positive")
	}
	return &Window{Width: width}, nil
}

// AddBPS records a directly measured bandwidth estimate.
func (w *Window) AddBPS(at time.Duration, bps float64) error {
	if bps < 0 {
		return timebase.NewError(timebase.CodeInvalidArgument, "negative bandwidth")
	}
	w.samples = append(w.samples, Sample{At: at, BPS: bps})
	return nil
}

// AddTransfer records bytes transferred over duration.
func (w *Window) AddTransfer(at, duration time.Duration, bytes int64) error {
	if duration <= 0 || bytes < 0 {
		return timebase.NewError(timebase.CodeInvalidArgument, "invalid transfer observation")
	}
	return w.AddBPS(at, float64(bytes*8)/duration.Seconds())
}

// Bandwidth returns the mean BPS in the active window. Observations at exactly
// now-Width are included.
func (w *Window) Bandwidth(now time.Duration) float64 {
	cutoff := now - w.Width
	var sum float64
	n := 0
	kept := w.samples[:0]
	for _, s := range w.samples {
		if s.At < cutoff {
			continue
		}
		kept = append(kept, s)
		sum += s.BPS
		n++
	}
	w.samples = kept
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// Decision is the selected output tier for one segment.
type Decision struct {
	BandwidthBPS float64 `json:"bandwidth_bps"`
	BudgetBPS    float64 `json:"budget_bps"`
	Bits         int     `json:"bits"`
	TargetBPS    int64   `json:"target_bps"`
	Delivered    bool    `json:"delivered"`
	Reason       string  `json:"reason,omitempty"`
}

// TargetBPS returns uncompressed PCM bitrate for the requested bit depth.
func TargetBPS(sampleRate, channels, bits int) (int64, error) {
	if sampleRate <= 0 || channels <= 0 || bits <= 0 {
		return 0, timebase.NewError(timebase.CodeInvalidArgument, "invalid PCM bitrate parameters")
	}
	v, ok := timebase.CheckedMul(int64(sampleRate), int64(channels))
	if !ok {
		return 0, timebase.NewError(timebase.CodeOverflow, "bitrate multiplication overflow")
	}
	v, ok = timebase.CheckedMul(v, int64(bits))
	if !ok {
		return 0, timebase.NewError(timebase.CodeOverflow, "bitrate multiplication overflow")
	}
	return v, nil
}

// AdaptWindow applies Adapt to the active sliding-window bandwidth estimate.
func AdaptWindow(w *Window, now time.Duration, sampleRate, channels int) (Decision, error) {
	if w == nil {
		return Decision{}, timebase.NewError(timebase.CodeInvalidArgument, "nil bandwidth window")
	}
	return Adapt(w.Bandwidth(now), sampleRate, channels)
}

// Adapt applies the 80% budget and chooses 24, then 16, otherwise no delivery.
func Adapt(bandwidthBPS float64, sampleRate, channels int) (Decision, error) {
	if bandwidthBPS < 0 {
		return Decision{}, timebase.NewError(timebase.CodeInvalidArgument, "negative bandwidth")
	}
	budget := bandwidthBPS * Utilization
	t24, err := TargetBPS(sampleRate, channels, 24)
	if err != nil {
		return Decision{}, err
	}
	t16, err := TargetBPS(sampleRate, channels, 16)
	if err != nil {
		return Decision{}, err
	}
	d := Decision{BandwidthBPS: bandwidthBPS, BudgetBPS: budget}
	switch {
	case budget >= float64(t24):
		d.Bits, d.TargetBPS, d.Delivered = 24, t24, true
	case budget >= float64(t16):
		d.Bits, d.TargetBPS, d.Delivered = 16, t16, true
		d.Reason = "24-bit exceeds 80% bandwidth budget"
	default:
		d.Bits, d.TargetBPS, d.Delivered = 0, 0, false
		d.Reason = "80% budget below minimum 16-bit tier"
	}
	return d, nil
}
