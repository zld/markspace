package ts

import "medkernel/media"

// PCRTracker unwraps raw PCR values onto a monotonic 90 kHz axis and enforces
// backstep/drift guardrails shared with the rest of the pipeline.
type PCRTracker struct {
	unw       *media.Unwrapper
	clock     *media.Clock
	nominal90 int64
	started   bool
	lastExt   int64
}

// NewPCRTracker builds a tracker. nominal is the expected tick delta between
// successive PCRs (0 disables the drift check); guardrails are in 90 kHz ticks.
func NewPCRTracker(nominal, maxBack, maxDrift int64) *PCRTracker {
	return &PCRTracker{
		unw:       media.NewUnwrapper(33, maxBack, 0),
		clock:     media.NewClock(maxBack, maxDrift),
		nominal90: nominal,
	}
}

// Push converts a raw 27 MHz PCR to extended 90 kHz ticks and verifies the
// delta. A backwards step beyond maxBack, or drift beyond maxDrift, yields a
// ClassClock error; the tracker state only advances on an accepted sample.
func (t *PCRTracker) Push(pcr27 int64, pid uint16, idx int64) (int64, error) {
	raw90 := uint64(media.PCRToTicks(pcr27))
	ext, err := t.unw.Push(raw90)
	if err != nil {
		return ext, media.Wrap(media.ClassClock, "ts.pcr", err,
			"PCR backwards beyond tolerance").WithPID(pid).WithIndex(idx)
	}
	if t.started && t.nominal90 > 0 {
		if derr := t.clock.DriftError(ext-t.lastExt, t.nominal90); derr != nil {
			return ext, media.Wrap(media.ClassClock, "ts.pcr", derr,
				"PCR drift exceeds threshold").WithPID(pid).WithIndex(idx)
		}
	}
	t.lastExt, t.started = ext, true
	return ext, nil
}

// PTSTracker unwraps 33-bit PES PTS values per elementary stream PID.
type PTSTracker struct {
	unw       *media.Unwrapper
	maxBack90 int64
}

// NewPTSTracker builds a PTS unwrapper with a backwards-step guardrail.
func NewPTSTracker(maxBack90 int64) *PTSTracker {
	return &PTSTracker{unw: media.NewUnwrapper(33, maxBack90, 0), maxBack90: maxBack90}
}

// Push extends a raw 33-bit PTS, including across the 2^33 boundary.
func (t *PTSTracker) Push(rawPTS uint64, pid uint16, idx int64) (int64, error) {
	ext, err := t.unw.Push(rawPTS)
	if err != nil {
		return ext, media.Wrap(media.ClassClock, "ts.pts", err,
			"PTS backwards beyond tolerance").WithPID(pid).WithIndex(idx)
	}
	return ext, nil
}
