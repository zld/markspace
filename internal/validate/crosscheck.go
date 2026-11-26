// Package validate cross-checks source metadata, canonical tracks, gaps and
// derived fragment timing/length before a manifest is accepted.
package validate

import (
	"sort"

	"mediacheck/internal/gap"
	"mediacheck/internal/timebase"
	"mediacheck/internal/track"
	"mediacheck/internal/wav"
)

// FragmentView is the package-neutral description of a derived fMP4 fragment.
type FragmentView struct {
	Index        int
	TrackID      string
	StartSample  int64
	FrameCount   int64
	SampleRate   int
	Channels     int
	Bits         int
	StartTick    int64
	EndTick      int64
	PayloadBytes int
}

// unionGapSamples totals missing samples without double-counting a late packet
// whose audit interval overlaps the earlier missing-sequence gap.
func unionGapSamples(gaps []gap.Gap) int64 {
	if len(gaps) == 0 {
		return 0
	}
	gs := append([]gap.Gap(nil), gaps...)
	sort.Slice(gs, func(i, j int) bool { return gs[i].StartSample < gs[j].StartSample })
	var total int64
	curStart, curEnd := gs[0].StartSample, gs[0].EndSample
	for _, g := range gs[1:] {
		switch {
		case g.StartSample > curEnd:
			total += curEnd - curStart
			curStart, curEnd = g.StartSample, g.EndSample
		case g.EndSample > curEnd:
			curEnd = g.EndSample
		}
	}
	total += curEnd - curStart
	return total
}

// SourceTrack checks WAV format fields against the canonical track.
func SourceTrack(meta wav.Metadata, tr *track.Track, allowGaps bool) error {
	if tr == nil {
		return timebase.NewError(timebase.CodeTrack, "nil canonical track")
	}
	if meta.SampleRate != tr.SampleRate {
		return timebase.NewError(timebase.CodeTrack, "sample rate mismatch").WithTrack(tr.ID)
	}
	if meta.Channels != tr.Mapping.SourceChannels {
		return timebase.NewError(timebase.CodeTrack, "channel count mismatch").WithTrack(tr.ID)
	}
	if meta.BitsPerSample != tr.SourceBits {
		return timebase.NewError(timebase.CodeTrack, "source bit depth mismatch").WithTrack(tr.ID)
	}
	if tr.TimelineEnd != meta.FrameCount {
		return timebase.NewError(timebase.CodeTrack, "timeline frame count mismatch").
			WithTrack(tr.ID).WithRange(0, tr.TimelineEnd)
	}
	missing := unionGapSamples(tr.Gaps)
	if allowGaps {
		if tr.Delivered+missing != meta.FrameCount {
			return timebase.NewError(timebase.CodeTrack, "delivered plus gap frames does not equal source frames").
				WithTrack(tr.ID)
		}
	} else if len(tr.Gaps) != 0 || tr.Delivered != meta.FrameCount {
		return timebase.NewError(timebase.CodeTrack, "complete source required but track has gaps").
			WithTrack(tr.ID)
	}
	return nil
}

// CanonicalTrack verifies mapping, chunk order, alignment and gap intervals.
func CanonicalTrack(tr *track.Track) error {
	if tr == nil {
		return timebase.NewError(timebase.CodeTrack, "nil track")
	}
	if tr.SampleRate <= 0 || tr.Channels < 1 || tr.Channels > wav.MaxChannels {
		return timebase.NewError(timebase.CodeTrack, "invalid rate/channels").WithTrack(tr.ID)
	}
	lastEnd := int64(0)
	for i, c := range tr.Chunks {
		if c.Index != i {
			return timebase.At(timebase.CodeTrack, c.SourceOffset, "chunk index discontinuity").WithTrack(tr.ID)
		}
		if c.StartSample < lastEnd {
			return timebase.At(timebase.CodeTrack, c.SourceOffset, "chunk overlaps or is unordered").
				WithTrack(tr.ID).WithRange(c.StartSample, c.EndSample())
		}
		if len(c.Samples)%tr.Channels != 0 || int64(len(c.Samples)/tr.Channels) != c.FrameCount {
			return timebase.At(timebase.CodeAlignment, c.SourceOffset, "chunk is not sample-frame aligned").
				WithTrack(tr.ID)
		}
		lastEnd = c.EndSample()
	}
	for _, g := range tr.Gaps {
		if g.StartSample < 0 || g.EndSample < g.StartSample {
			return timebase.NewError(timebase.CodeTrack, "invalid gap interval").WithTrack(tr.ID)
		}
		if g.StartTick < 0 || g.EndTick < g.StartTick {
			return timebase.NewError(timebase.CodeTrack, "invalid gap tick interval").WithTrack(tr.ID)
		}
	}
	if !sort.SliceIsSorted(tr.Gaps, func(i, j int) bool { return tr.Gaps[i].StartSample < tr.Gaps[j].StartSample }) {
		return timebase.NewError(timebase.CodeTrack, "gaps are not sorted").WithTrack(tr.ID)
	}
	return nil
}

func intersectsGap(tr *track.Track, start, end int64) bool {
	for _, g := range tr.Gaps {
		if start < g.EndSample && g.StartSample < end {
			return true
		}
	}
	return false
}

func coveredByChunks(tr *track.Track, start, end int64) bool {
	cur := start
	for _, c := range tr.Chunks {
		if c.EndSample() <= cur {
			continue
		}
		if c.StartSample > cur {
			return false
		}
		if c.EndSample() >= end {
			return true
		}
		cur = c.EndSample()
	}
	return cur >= end
}

// FragmentAgainstTrack rechecks one derived fragment before it is published.
func FragmentAgainstTrack(f FragmentView, tr *track.Track) error {
	if tr == nil {
		return timebase.NewError(timebase.CodeFragment, "nil track")
	}
	if f.TrackID != "" && f.TrackID != tr.ID {
		return timebase.NewError(timebase.CodeFragment, "fragment track id mismatch").WithTrack(tr.ID)
	}
	if f.SampleRate != tr.SampleRate || f.Channels != tr.Channels {
		return timebase.NewError(timebase.CodeFragment, "fragment format does not match canonical track").
			WithTrack(tr.ID)
	}
	if f.Bits != 16 && f.Bits != 24 {
		return timebase.NewError(timebase.CodeFragment, "output bit depth is not 16 or 24").WithTrack(tr.ID)
	}
	if f.StartSample < 0 || f.FrameCount <= 0 {
		return timebase.NewError(timebase.CodeFragment, "fragment interval invalid").WithTrack(tr.ID)
	}
	end := f.StartSample + f.FrameCount
	wantBytes, ok := timebase.CheckedMul(f.FrameCount, int64(f.Channels*(f.Bits/8)))
	if !ok || wantBytes != int64(f.PayloadBytes) {
		return timebase.NewError(timebase.CodeFragment,
			"fragment payload length %d violates frame alignment want %d", f.PayloadBytes, wantBytes).
			WithTrack(tr.ID).WithRange(f.StartTick, f.EndTick)
	}
	wantStartTick, err := timebase.SamplesToTicks(f.StartSample, uint32(f.SampleRate), timebase.TickRate)
	if err != nil {
		return err
	}
	wantEndTick, err := timebase.SamplesToTicks(end, uint32(f.SampleRate), timebase.TickRate)
	if err != nil {
		return err
	}
	if f.StartTick != wantStartTick || f.EndTick != wantEndTick || f.EndTick <= f.StartTick {
		return timebase.NewError(timebase.CodeFragment,
			"fragment timestamps [%d,%d) do not match rational sample ticks [%d,%d)",
			f.StartTick, f.EndTick, wantStartTick, wantEndTick).
			WithTrack(tr.ID).WithRange(f.StartTick, f.EndTick)
	}
	if intersectsGap(tr, f.StartSample, end) {
		return timebase.NewError(timebase.CodeFragment, "fragment crosses an explicitly recorded gap").
			WithTrack(tr.ID).WithRange(f.StartTick, f.EndTick)
	}
	if !coveredByChunks(tr, f.StartSample, end) {
		return timebase.NewError(timebase.CodeFragment, "fragment references samples absent from canonical chunks").
			WithTrack(tr.ID).WithRange(f.StartTick, f.EndTick)
	}
	return nil
}
