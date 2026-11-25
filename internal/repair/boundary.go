// Package repair turns recoverable boundary-jump errors into explicit,
// traceable timeline corrections. Every repair keeps the raw clock value,
// the applied offset and the originating packet/sample position, so an
// archived result can always be traced back to its uncorrected input.
package repair

import (
	"fmt"

	"mediavault/internal/timeline"
)

// Correction is one applied boundary repair.
type Correction struct {
	Track        string `json:"track"`
	Position     uint64 `json:"position"`      // packet/sample position on the track
	ByteOffset   int64  `json:"byte_offset"`   // originating byte offset when known
	RawDTS       int64  `json:"raw_dts"`       // uncorrected clock ticks
	OffsetTicks  int64  `json:"offset_ticks"`  // signed correction applied
	CorrectedDTS int64  `json:"corrected_dts"` // ticks after correction
	Timescale    uint32 `json:"timescale"`
	Recovered    bool   `json:"recovered"`
}

// FailedTrack records a track that could not be repaired. It is retained
// rather than dropped, satisfying the no-silent-discard requirement.
type FailedTrack struct {
	Track  string `json:"track"`
	Reason string `json:"reason"`
	Kind   string `json:"kind"`
}

// BoundaryRepairer owns the per-track normalizers during a run and decides
// whether each boundary jump gets a deterministic correction or fails the
// track. One instance is safe for sequential, single-goroutine use.
type BoundaryRepairer struct {
	norms       map[string]*timeline.TrackNormalizer
	corrections []Correction
	failed      []FailedTrack
	// activeOffset is the signed tick shift currently applied to a track
	// after a source switch; subsequent packets on the new clock inherit
	// it until another jump changes the lineage.
	activeOffset map[string]int64
	// MaxBackwardTicks bounds a repairable excursion; larger resets are
	// treated as a different clock lineage and fail the track.
	MaxBackwardTicks int64
}

// NewBoundaryRepairer builds a repairer with the given repairable bound.
func NewBoundaryRepairer(maxBackwardTicks int64) *BoundaryRepairer {
	if maxBackwardTicks <= 0 {
		maxBackwardTicks = 1 << 32
	}
	return &BoundaryRepairer{
		norms:            map[string]*timeline.TrackNormalizer{},
		activeOffset:     map[string]int64{},
		MaxBackwardTicks: maxBackwardTicks,
	}
}

// Register attaches the normalizer owned for a track.
func (b *BoundaryRepairer) Register(id timeline.TrackID, n *timeline.TrackNormalizer) {
	b.norms[id.String()] = n
}

// Handle applies a deterministic correction for a recoverable jump. The
// offset shifts the jumped raw clock just past the last good timestamp so
// monotonicity is restored while the raw value and offset are both kept.
// If the excursion exceeds MaxBackwardTicks the track is marked failed and
// ErrTrackFailed is returned; the track is not dropped silently.
func (b *BoundaryRepairer) Handle(se *timeline.StageError, jumpedRaw int64) (*timeline.Timestamp, error) {
	key := se.Track.String()
	norm, ok := b.norms[key]
	if !ok {
		return nil, fmt.Errorf("repair: no normalizer registered for %s", key)
	}
	last := int64(norm.LastExpanded())
	backward := last - jumpedRaw
	if backward > b.MaxBackwardTicks {
		reason := fmt.Sprintf("boundary excursion %d ticks exceeds repairable bound %d", backward, b.MaxBackwardTicks)
		norm.Fail(reason)
		b.failed = append(b.failed, FailedTrack{Track: key, Reason: reason, Kind: timeline.KindUnrecoverable})
		return nil, timeline.ErrTrackFailed
	}
	// Shift the new clock to continue immediately after the last good tick.
	offset := (last + 1) - jumpedRaw
	corrected := norm.ApplyCorrection(jumpedRaw, offset, se.Position)
	b.activeOffset[key] = offset
	b.corrections = append(b.corrections, Correction{
		Track:        key,
		Position:     se.Position,
		ByteOffset:   se.Offset,
		RawDTS:       jumpedRaw,
		OffsetTicks:  offset,
		CorrectedDTS: corrected.RawDTS,
		Timescale:    corrected.Timescale,
		Recovered:    true,
	})
	return corrected, nil
}

// Adapt applies the currently-active source-switch offset to a raw tick on
// the given track, returning the value to feed into the normalizer. Tracks
// that never switched return the raw value unchanged.
func (b *BoundaryRepairer) Adapt(id timeline.TrackID, raw int64) int64 {
	if off, ok := b.activeOffset[id.String()]; ok {
		return raw + off
	}
	return raw
}

// ActiveOffset reports the offset currently applied to a track (0 if none).
func (b *BoundaryRepairer) ActiveOffset(id timeline.TrackID) int64 {
	return b.activeOffset[id.String()]
}

// Corrections returns every applied repair in application order.
func (b *BoundaryRepairer) Corrections() []Correction { return b.corrections }

// Failed returns the tracks that could not be repaired.
func (b *BoundaryRepairer) Failed() []FailedTrack { return b.failed }
