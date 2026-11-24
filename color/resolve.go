// Package color resolves conflicting colour metadata between track-level and
// container-level sources. Unknown enumerations are retained verbatim and any
// unsafe conversion is blocked rather than guessed.
package color

import (
	"fmt"

	"mediacheck/ebml"
	"mediacheck/timeline"
)

// Source selects where a resolved colour record came from.
type Source string

const (
	SourceTrack     Source = "track"
	SourceContainer Source = "container"
	SourceDefault   Source = "default"
)

// Known enumerations the tool can safely interpret. Values outside these
// sets are treated as opaque and block conversion.
var (
	knownMatrices    = map[uint32]bool{1: true, 6: true, 9: true, 10: true}
	knownTransfers   = map[uint32]bool{1: true, 6: true, 16: true, 18: true}
	knownPrimaries   = map[uint32]bool{1: true, 6: true, 9: true}
)

// Policy orders the metadata sources. The first entry present wins.
type Policy struct {
	PreferTrack bool // true: track before container; false: container first
}

// Record is the resolved colour description with full provenance.
type Record struct {
	Matrix       uint32
	Transfer     uint32
	Primaries    uint32
	RangeFlag    uint32
	RawEnum      uint32 // non-zero when an unknown enumeration was retained
	Source       Source
	SourceOffset int64
	// Conflict is true when both sources existed and disagreed on a field.
	Conflict bool
}

// DefaultRecord is the conservative sRGB-ish fallback.
var DefaultRecord = Record{
	Matrix: 1, Transfer: 1, Primaries: 1, RangeFlag: 1, Source: SourceDefault,
}

// Resolve picks one colour record according to policy, records every conflict
// and reports whether a safe conversion to target is possible.
func Resolve(track, container *ebml.ColorMeta, policy Policy) (*Record, error) {
	if track == nil && container == nil {
		d := DefaultRecord
		return &d, nil
	}
	conflict := track != nil && container != nil &&
		(track.Matrix != container.Matrix ||
			track.Transfer != container.Transfer ||
			track.Primaries != container.Primaries ||
			track.RangeFlag != container.RangeFlag)

	first, second := orderSources(track, container, policy)
	chosen := pick(first)
	if chosen == nil {
		chosen = pick(second)
	}
	if chosen == nil {
		d := DefaultRecord
		return &d, nil
	}
	chosen.Conflict = conflict
	return chosen, nil
}

// SafeToConvert reports whether every enumeration is known and internally
// consistent enough for a colour-space conversion. An unknown raw enumeration
// or unknown standard value always blocks conversion.
func (r *Record) SafeToConvert() (bool, string) {
	if r.RawEnum != 0 {
		return false, fmt.Sprintf("unknown retained colour enumeration %d", r.RawEnum)
	}
	if !knownMatrices[r.Matrix] {
		return false, fmt.Sprintf("unknown matrix coefficients %d", r.Matrix)
	}
	if !knownTransfers[r.Transfer] {
		return false, fmt.Sprintf("unknown transfer characteristics %d", r.Transfer)
	}
	if !knownPrimaries[r.Primaries] {
		return false, fmt.Sprintf("unknown colour primaries %d", r.Primaries)
	}
	return true, ""
}

func orderSources(track, container *ebml.ColorMeta, policy Policy) (*ebml.ColorMeta, *ebml.ColorMeta) {
	if policy.PreferTrack {
		return track, container
	}
	return container, track
}

func pick(meta *ebml.ColorMeta) *Record {
	if meta == nil {
		return nil
	}
	var src Source
	switch meta.SourceElement {
	case ebml.IDContainerCol:
		src = SourceContainer
	default:
		src = SourceTrack
	}
	return &Record{
		Matrix:       meta.Matrix,
		Transfer:     meta.Transfer,
		Primaries:    meta.Primaries,
		RangeFlag:    meta.RangeFlag,
		RawEnum:      meta.RawEnum,
		Source:       src,
		SourceOffset: meta.SourceOffset,
	}
}

// RequireSafe is a convenience for pipeline stages that must not publish an
// output after an unsafe colour decision.
func (r *Record) RequireSafe() error {
	if ok, why := r.SafeToConvert(); !ok {
		return timeline.New(timeline.CatUnsupported, "unsafe colour conversion: "+why).
			WithOffset(r.SourceOffset)
	}
	return nil
}
