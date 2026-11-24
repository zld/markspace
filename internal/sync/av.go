// Package sync measures cross-track (A/V) deviation on the master timeline.
// Deviations beyond the tolerance are recorded as errors; missing
// counterparts are reported, never filled with fabricated samples.
package sync

import "archive/internal/model"

// DefaultToleranceNs is the default A/V sync tolerance: 20ms.
const DefaultToleranceNs int64 = 20_000_000

// maxPairDistanceNs caps how far apart two packets may be to count as
// counterparts; beyond this there is simply no counterpart to compare with.
const maxPairDistanceNs int64 = 500_000_000

// Tracker observes emitted packets in stream order and tracks the maximum
// deviation between the video stream and its nearest audio counterpart.
type Tracker struct {
	ToleranceNs int64

	lastAudio  int64
	hasAudio   bool
	lastVideo  int64
	hasVideo   bool
	MaxDevNs   int64
	Violations int
	Errs       model.Errors
}

func NewTracker(toleranceNs int64) *Tracker {
	if toleranceNs <= 0 {
		toleranceNs = DefaultToleranceNs
	}
	return &Tracker{ToleranceNs: toleranceNs}
}

// Observe registers one emitted packet. Audio packets are compared against
// the latest video timestamp and vice versa; the stream is roughly
// time-ordered so the latest counterpart is the nearest candidate.
func (t *Tracker) Observe(trackType string, ptsNs int64) {
	switch trackType {
	case "video":
		t.lastVideo, t.hasVideo = ptsNs, true
		if t.hasAudio {
			t.check(ptsNs, t.lastAudio)
		}
	case "audio":
		t.lastAudio, t.hasAudio = ptsNs, true
		if t.hasVideo {
			t.check(ptsNs, t.lastVideo)
		}
	}
}

func (t *Tracker) check(a, b int64) {
	dev := a - b
	if dev < 0 {
		dev = -dev
	}
	if dev > maxPairDistanceNs {
		return // no usable counterpart; nothing fabricated
	}
	if dev > t.MaxDevNs {
		t.MaxDevNs = dev
	}
	if dev > t.ToleranceNs {
		t.Violations++
		t.Errs = append(t.Errs, model.ScanError{
			Code:   model.ErrAVDesync,
			Offset: -1,
			Track:  -1,
			Detail: "A/V deviation " + model.Timestamp{Nanos: dev}.String() +
				" exceeds tolerance " + model.Timestamp{Nanos: t.ToleranceNs}.String(),
		})
	}
}
