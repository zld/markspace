package scan

import (
	"mediacore/internal/clock"
)

// SeekResult quantifies a random-access request. Target is the sample the
// caller wanted; Anchor is the preceding independently-decodable sample the
// decoder must actually start from. FallbackSamples/FallbackNanos quantify
// how far backwards the decoder has to go.
type SeekResult struct {
	TargetIndex     uint32
	AnchorIndex     uint32
	FallbackSamples int64
	FallbackNanos   clock.Nanos
}

// SeekDecodable resolves a random-access target against a sync-set supplied
// as 1-based sample numbers. When no sync sample exists at or before the
// target, a recoverable missing-keyframe error is returned naming the track.
func SeekDecodable(trackID, target uint32, total uint32, syncNumbers []uint32, dts func(uint32) clock.Nanos) (SeekResult, error) {
	if target >= total {
		return SeekResult{}, clock.WithTrack(clock.ErrInvalidArgument(
			"seek target %d out of range [0,%d)", target, total), trackID, int64(target))
	}
	anchor := int64(-1)
	for _, n := range syncNumbers { // stss numbers are strictly increasing
		idx := int64(n) - 1
		if idx <= int64(target) {
			anchor = idx
			continue
		}
		break
	}
	if anchor < 0 {
		return SeekResult{}, clock.WithTrack(clock.ErrRecoverablef(
			"scan.stss", "no decodable key frame at or before target sample %d", target),
			trackID, int64(target))
	}
	targetDTS := dts(target)
	anchorDTS := dts(uint32(anchor))
	fallback := targetDTS - anchorDTS
	if fallback < 0 {
		return SeekResult{}, clock.ErrInvariantf("scan.stss", "anchor DTS after target DTS")
	}
	return SeekResult{
		TargetIndex:     target,
		AnchorIndex:     uint32(anchor),
		FallbackSamples: int64(target) - anchor,
		FallbackNanos:   fallback,
	}, nil
}

// SyncNumbers returns the sorted 1-based sync sample numbers for this track.
func (t *TrackScan) SyncNumbers() []uint32 {
	out := make([]uint32, 0, len(t.tables.sync))
	for n := range t.tables.sync {
		out = append(out, n)
	}
	// Map keys are unordered; sort for deterministic output.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
