// Package sync builds one common audio/video timeline, measures inter-track
// drift on the exact rational master clock and applies bounded drift
// correction. Errors beyond the explicit tolerance are surfaced rather than
// silently resampled.
package sync

import (
	"fmt"

	"mediaqc/internal/clock"
	"mediaqc/internal/model"
)

// Correction records one applied alignment decision for the evidence report.
type Correction struct {
	TrackID         string
	OffsetNanos     int64
	DriftMillis     float64
	WithinTolerance bool
	Action          string // "none", "shift", "rejected"
}

// Report is the per-material sync evidence.
type Report struct {
	Corrections     []Correction
	MaxAbsDriftMs   float64
	SyncErrorMs     float64 // signed residual on the last anchor
	Rejected        bool
}

// Aligner owns the tolerance (in nanoseconds) and a reference timeline.
type Aligner struct {
	materialID       string
	toleranceNanos   int64
	referenceTrackID string
	// first anchor instant per track on the master clock.
	anchors map[string]clock.Instant
}

// NewAligner builds an aligner. toleranceMs is the explicit drift tolerance.
func NewAligner(materialID string, toleranceMs int64) (*Aligner, error) {
	if toleranceMs < 0 {
		return nil, model.NewError(model.CodeConfig, model.StageSync, materialID, "",
			"negative sync tolerance")
	}
	return &Aligner{
		materialID:     materialID,
		toleranceNanos: toleranceMs * 1_000_000,
		anchors:        map[string]clock.Instant{},
	}, nil
}

// Anchor registers the first timeline point of a track, projected onto the
// master clock. The first track registered becomes the reference.
func (a *Aligner) Anchor(trackID string, first clock.Instant) {
	if _, exists := a.anchors[trackID]; exists {
		return
	}
	if a.referenceTrackID == "" {
		a.referenceTrackID = trackID
	}
	a.anchors[trackID] = first.Convert(clock.Master)
}

// Align computes the offset of every track against the reference and decides
// whether each can be corrected within tolerance.
func (a *Aligner) Align() (*Report, error) {
	if a.referenceTrackID == "" {
		return nil, model.NewError(model.CodeConfig, model.StageSync, a.materialID, "",
			"no track anchors registered")
	}
	ref := a.anchors[a.referenceTrackID]
	rep := &Report{}
	// Deterministic iteration order for identical reruns.
	trackIDs := make([]string, 0, len(a.anchors))
	for id := range a.anchors {
		trackIDs = append(trackIDs, id)
	}
	for i := 1; i < len(trackIDs); i++ { // simple insertion sort
		for j := i; j > 0 && trackIDs[j-1] > trackIDs[j]; j-- {
			trackIDs[j-1], trackIDs[j] = trackIDs[j], trackIDs[j-1]
		}
	}
	for _, id := range trackIDs {
		cur := a.anchors[id]
		delta, ok := clock.DeltaNanos(ref, cur)
		if !ok {
			return nil, model.NewError(model.CodeTimestampOverflow, model.StageSync,
				a.materialID, id, "timestamp difference overflows int64")
		}
		abs := delta
		if abs < 0 {
			abs = -abs
		}
		driftMs := clock.DeltaMillis(ref, cur)
		c := Correction{TrackID: id, OffsetNanos: delta, DriftMillis: driftMs}
		switch {
		case id == a.referenceTrackID:
			c.Action, c.WithinTolerance = "none", true
		case abs <= a.toleranceNanos:
			c.Action, c.WithinTolerance = "shift", true
		default:
			c.Action, c.WithinTolerance = "rejected", false
			rep.Rejected = true
		}
		rep.Corrections = append(rep.Corrections, c)
		if neg := -driftMs; neg > driftMs {
			driftMs = neg
		}
		if driftMs > rep.MaxAbsDriftMs {
			rep.MaxAbsDriftMs = driftMs
		}
	}
	if len(rep.Corrections) > 0 {
		rep.SyncErrorMs = rep.Corrections[len(rep.Corrections)-1].DriftMillis
	}
	return rep, nil
}

// ApplyOffset projects an event's start/PTS onto the corrected common
// timeline. The shift is expressed in master nanoseconds and converted back
// into the event's own timebase so source ordering is preserved.
func ApplyOffset(ev model.Event, offsetNanos int64) model.Event {
	masterStart := ev.Start.Convert(clock.Master)
	shifted := clock.Master.At(masterStart.Ticks() + offsetNanos).Convert(ev.Timebase)
	ev.Start = shifted
	ev.PTS = shifted
	return ev
}

// CheckMonotonic verifies that timestamps never go backwards within a track.
func CheckMonotonic(materialID, trackID string, evs []model.Event) error {
	for i := 1; i < len(evs); i++ {
		if clock.Cmp(evs[i-1].Start, evs[i].Start) > 0 {
			return model.NewError(model.CodeTimestampJump, model.StageSync, materialID, trackID,
				fmt.Sprintf("non-monotonic timestamp between seq %d and %d",
					evs[i-1].Sequence, evs[i].Sequence))
		}
	}
	return nil
}
