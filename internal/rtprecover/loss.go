package rtprecover

import (
	"github.com/archive/mediakernel/internal/manifest"
	"github.com/archive/mediakernel/internal/timeline"
)

// Detector watches the in-order output stream and converts missing sequence
// runs into explicit gap records. A run is only confirmed when the stream has
// moved past it: a gap whose packets might still arrive inside the reorder
// window stays "open" and is never reported, so recovery never mistakes
// reordering for loss.
type Detector struct {
	threshold int // minimum missing count that qualifies as a burst

	havePrev bool
	prevSeq  uint16
	prevUS   int64 // unified-axis time of the last released packet

	// Open hole state: the first missing sequence after prevSeq.
	holeOpen     bool
	holeFirst    uint16
	holeUS       int64 // estimated start time of the hole
	holeCount    int
	confirmed    []manifest.LossGap
	totalMissing int
}

// NewDetector creates a burst detector. threshold is the minimum number of
// consecutively missing packets required to emit a gap record; isolated
// single-packet losses below the threshold are counted but not reported.
func NewDetector(threshold int) (*Detector, error) {
	if threshold <= 0 {
		return nil, timeline.NewError(timeline.KindInvalidInput, "loss threshold must be positive")
	}
	return &Detector{threshold: threshold}, nil
}

// Observe feeds one in-order packet (the output of Window) into the detector.
// RTP timestamps advance one clock tick per sequence number for the generated
// fixtures; the caller passes the unified-axis time so the detector does no
// clock arithmetic of its own.
func (d *Detector) Observe(op OrderedPacket) {
	seq := op.Packet.SequenceNumber
	nowUS := op.TimelineUS
	if !d.havePrev {
		d.havePrev = true
		d.prevSeq = seq
		d.prevUS = nowUS
		return
	}
	dist := SeqDistance(d.prevSeq, seq)
	switch {
	case dist == 1:
		// Contiguous: any open hole is now confirmed closed.
		d.closeHole(nowUS)
	case dist > 1:
		// Missing run of dist-1 sequences between prev and this packet.
		missing := dist - 1
		if !d.holeOpen {
			d.holeOpen = true
			d.holeFirst = d.prevSeq + 1
			d.holeUS = d.prevUS
			d.holeCount = 0
		}
		d.holeCount += missing
		d.totalMissing += missing
		// The arriving packet proves the hole will never fill: confirm it.
		d.closeHole(nowUS)
	case dist <= 0:
		// The window guarantees strictly increasing output; anything else is a
		// caller contract violation and is ignored defensively rather than
		// corrupting gap bookkeeping.
		return
	}
	d.prevSeq = seq
	d.prevUS = nowUS
}

// closeHole finalizes the open hole if it reached the burst threshold.
func (d *Detector) closeHole(endUS int64) {
	if !d.holeOpen {
		return
	}
	if d.holeCount >= d.threshold {
		d.confirmed = append(d.confirmed, manifest.LossGap{
			StartUS:      d.holeUS,
			EndUS:        endUS,
			MissingCount: d.holeCount,
		})
	}
	d.holeOpen = false
	d.holeCount = 0
}

// Finish closes any trailing hole using the supplied end-of-stream time. It is
// called after the window has been flushed; the end time is the timeline end of
// the last observed packet so a tail loss still gets bounded coordinates.
func (d *Detector) Finish(streamEndUS int64) []manifest.LossGap {
	if d.holeOpen {
		d.closeHole(streamEndUS)
	}
	out := make([]manifest.LossGap, len(d.confirmed))
	copy(out, d.confirmed)
	return out
}

// Gaps returns all confirmed burst gaps in stream order.
func (d *Detector) Gaps() []manifest.LossGap {
	out := make([]manifest.LossGap, len(d.confirmed))
	copy(out, d.confirmed)
	return out
}

// TotalMissing is the count of every missing sequence observed, including
// sub-threshold losses that did not become gap records.
func (d *Detector) TotalMissing() int { return d.totalMissing }

// Covers reports whether the timeline interval [startUS, endUS) is overlapped
// by any confirmed gap. Silence detection and fingerprint merging use this to
// refuse classifying or joining across unknown content.
func Covers(gaps []manifest.LossGap, startUS, endUS int64) bool {
	for _, g := range gaps {
		if startUS < g.EndUS && g.StartUS < endUS {
			return true
		}
	}
	return false
}
