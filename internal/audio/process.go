package audio

import (
	"fmt"

	"mediacore/internal/timeline"
)

// SegmentProcessor applies rate conversion, layout normalization, and gain to
// a sequence of contiguous segments without ever bridging a gap.
//
// Reset policy (fixed, documented, and counted):
//   - every segment starts with zeroed resampler history;
//   - gain is computed once over the concatenation of all segments so the
//     output has a single consistent level;
//   - no cross-fade, interpolation, or speculative sample is generated across
//     a gap.
type SegmentProcessor struct {
	InRate    int
	OutRate   int
	InLayout  Layout
	OutLayout Layout
	Gain      GainRequest
	Resampler Resampler
}

// SegmentInput is one verified contiguous run of decoded PCM.
type SegmentInput struct {
	Buffer     *Buffer
	StartTicks timeline.Ticks
}

// SegmentOutput is one processed run positioned on the output tick grid.
type SegmentOutput struct {
	Buffer     *Buffer
	StartTicks timeline.Ticks
}

// ProcessResult carries the processed segments, the applied gain, the
// measurement before and after, and accumulated quality statistics.
type ProcessResult struct {
	Segments []SegmentOutput
	Input    LoudnessResult
	Output   LoudnessResult
	GainDB   float64
	Limited  bool
	Stats    Stats
	Resets   int
}

// Process runs the audio chain. It returns an error only for structural
// problems (bad rates, unmappable layouts, invalid buffers); damaged input
// regions are represented by the segments themselves, never by errors.
func (p SegmentProcessor) Process(segs []SegmentInput) (*ProcessResult, error) {
	if p.InRate <= 0 || p.OutRate <= 0 {
		return nil, fmt.Errorf("audio: invalid process rates %d -> %d", p.InRate, p.OutRate)
	}
	if !p.InLayout.Valid() || !p.OutLayout.Valid() {
		return nil, fmt.Errorf("audio: invalid input or output layout")
	}
	res := &ProcessResult{}
	if p.Resampler.HalfTaps == 0 {
		p.Resampler = NewResampler()
	}

	// Pass 1: rate-convert and remap each segment independently.
	type staged struct {
		out *Buffer
		at  timeline.Ticks
	}
	var stage []staged
	var joined []*Buffer
	for _, s := range segs {
		if s.Buffer == nil || s.Buffer.Frames == 0 {
			continue
		}
		if s.Buffer.Layout.Name != p.InLayout.Name {
			return nil, fmt.Errorf("audio: segment layout %s does not match input layout %s",
				s.Buffer.Layout.Name, p.InLayout.Name)
		}
		rs, stats, err := p.Resampler.ResampleAt(s.Buffer, p.InRate, p.OutRate)
		if err != nil {
			return nil, err
		}
		// Every segment is processed with fresh filter state. A rate change
		// resets the resampler (reported by it), and a segment boundary resets
		// adjacency even at equal rates, so the count is one per segment.
		res.Stats.Resets++
		res.Resets++
		res.Stats.InputFrames += stats.InputFrames
		res.Stats.OutputFrames += stats.OutputFrames
		res.Stats.TailFrames += stats.TailFrames
		res.Stats.Segments++
		mapped, err := ConvertLayout(rs, p.OutLayout)
		if err != nil {
			return nil, err
		}
		at, err := timeline.Convert(s.StartTicks, timeline.MustScale(int64(p.InRate)), timeline.MustScale(int64(p.OutRate)))
		if err != nil {
			return nil, err
		}
		mapped.Start = int64(at)
		stage = append(stage, staged{out: mapped, at: at})
		joined = append(joined, rs)
	}
	if len(stage) == 0 {
		res.Input = Measure(&Buffer{Layout: p.OutLayout, Frames: 0})
		res.Output = res.Input
		return res, nil
	}

	// Gain is measured on the joined input so a single gain serves the whole
	// run and no segment gets a different level.
	concat, err := AppendBuffers(joined...)
	if err != nil {
		return nil, err
	}
	res.Input = MeasureAt(concat, p.OutRate)
	gainDB, limited, err := gainFor(res.Input, p.Gain)
	if err != nil {
		return nil, err
	}
	res.GainDB = gainDB
	res.Limited = limited

	// Pass 2: apply the same linear gain to every segment and account.
	factor := dbToLinear(gainDB)
	for _, st := range stage {
		for i := range st.out.Data {
			st.out.Data[i] *= factor
		}
		if err := st.out.Validate(); err != nil {
			return nil, err
		}
		for f := 0; f < st.out.Frames; f++ {
			res.Stats.Observe(st.out.Frame(f))
		}
		res.Segments = append(res.Segments, SegmentOutput{Buffer: st.out, StartTicks: st.at})
	}
	var all []*Buffer
	for _, s := range res.Segments {
		all = append(all, s.Buffer)
	}
	joinedOut, err := AppendBuffers(all...)
	if err != nil {
		return nil, err
	}
	res.Output = MeasureAt(joinedOut, p.OutRate)
	if err := res.Stats.Validate(); err != nil {
		return nil, err
	}
	return res, nil
}
