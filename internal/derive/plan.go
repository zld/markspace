package derive

import (
	"mediacheck/internal/timebase"
	"mediacheck/internal/track"
)

// PlannedRange is a gap-free fragment range and its original PCM byte span.
type PlannedRange struct {
	Index        int
	StartSample  int64
	FrameCount   int64
	SourceOffset int64
	SourceLength int64
}

// PlanSegments splits contiguous chunk runs into at most maxFrames fragments.
// Chunks separated by a gap are never placed in the same segment.
func PlanSegments(tr *track.Track, maxFrames int64) ([]PlannedRange, error) {
	if maxFrames <= 0 {
		maxFrames = 48000
	}
	blockAlign := int64(tr.Mapping.SourceChannels * (tr.SourceBits / 8))
	if blockAlign <= 0 {
		return nil, timebase.NewError(timebase.CodeTrack, "cannot plan source offset without source bit depth")
	}
	var out []PlannedRange
	var runStart, runEnd, runSourceOffset int64
	have := false
	flush := func() error {
		if !have {
			return nil
		}
		for start := runStart; start < runEnd; start += maxFrames {
			frames := min64(maxFrames, runEnd-start)
			delta := start - runStart
			out = append(out, PlannedRange{
				Index:        len(out),
				StartSample:  start,
				FrameCount:   frames,
				SourceOffset: runSourceOffset + delta*blockAlign,
				SourceLength: frames * blockAlign,
			})
		}
		have = false
		return nil
	}

	for _, c := range tr.Chunks {
		if have && c.StartSample != runEnd {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		if !have {
			runStart = c.StartSample
			runEnd = c.EndSample()
			// Chunk source offset points at c.StartSample.
			runSourceOffset = c.SourceOffset
			have = true
		} else {
			runEnd = c.EndSample()
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Index = i
	}
	return out, nil
}

// BuildAll plans and builds every valid fragment. Corrupt or gapped ranges do
// not produce a fragment because validation rejects absent/gap-crossing data.
func BuildAll(tr *track.Track, outputBits int, maxFrames int64) ([]*Segment, error) {
	plans, err := PlanSegments(tr, maxFrames)
	if err != nil {
		return nil, err
	}
	out := make([]*Segment, 0, len(plans))
	for i, p := range plans {
		seg, err := BuildFragment(tr, p.StartSample, p.FrameCount, p.SourceOffset, p.SourceLength, BuildOptions{
			OutputBits: outputBits,
			Sequence:   uint32(i + 1),
		})
		if err != nil {
			return nil, err
		}
		out = append(out, seg)
	}
	return out, nil
}
