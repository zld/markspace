package audio

import "medpipe/timeline"

// StageStats holds the cross-stage frame counters used to verify the
// input-frames / resampled-frames / interval / manifest-duration invariant.
type StageStats struct {
	InputFrames     int64
	ResampledFrames int64
	OutputFrames    int64
	ClippedSamples  int64
}

// Pipeline is the per-track stateful chain: PCM -> resample -> layout ->
// loudness limit. Every stage keeps its state on the Pipeline so blocks
// submitted at different times are processed as one continuous stream.
type Pipeline struct {
	TrackID    string
	InRate     uint32
	OutRate    uint32
	InLayout   string
	OutLayout  string
	TargetLUFS float64
	resampler  *Resampler
	matrix     *Matrix
	inSpec     LayoutSpec
	outSpec    LayoutSpec
	stats      StageStats
	lastLoud   LoudnessReport
}

// NewPipeline validates every stage's configuration up front.
func NewPipeline(trackID string, inRate, outRate uint32, inLayout, outLayout string, targetLUFS float64) (*Pipeline, error) {
	rs, err := NewResampler(inRate, outRate, mustChannels(inLayout))
	if err != nil {
		return nil, err
	}
	m, err := BuildMatrix(inLayout, outLayout)
	if err != nil {
		return nil, err
	}
	inSpec, err := LookupLayout(inLayout)
	if err != nil {
		return nil, err
	}
	outSpec, err := LookupLayout(outLayout)
	if err != nil {
		return nil, err
	}
	return &Pipeline{
		TrackID: trackID, InRate: inRate, OutRate: outRate,
		InLayout: inLayout, OutLayout: outLayout, TargetLUFS: targetLUFS,
		resampler: rs, matrix: m, inSpec: inSpec, outSpec: outSpec,
	}, nil
}

func mustChannels(layout string) int {
	spec, err := LookupLayout(layout)
	if err != nil {
		return 1 // NewResampler rejects zero; real error surfaces in BuildMatrix
	}
	return spec.Channels
}

// BlockResult is one block's output plus its processing statistics.
type BlockResult struct {
	Samples      []float64 // interleaved, output layout
	Frames       int
	InputFrames  int
	Loudness     LoudnessReport
	Interval     timeline.Interval // output-grid interval of this block
}

// ProcessBlock runs one raw PCM block through the full chain.
func (p *Pipeline) ProcessBlock(pcm []byte, bits uint16, startSample int64) (*BlockResult, error) {
	samples, err := DecodePCM(pcm, bits, uint16(p.inSpec.Channels))
	if err != nil {
		return nil, err
	}
	inFrames := len(samples) / p.inSpec.Channels
	if inFrames == 0 {
		return &BlockResult{Samples: []float64{}, Frames: 0, InputFrames: 0,
			Interval: timeline.Interval{Start: startSample, End: startSample}}, nil
	}
	p.stats.InputFrames += int64(inFrames)
	frames := Frames(samples, p.inSpec.Channels)
	rsFrames, err := p.resampler.Process(frames)
	if err != nil {
		return nil, err
	}
	outN := 0
	if len(rsFrames) > 0 {
		outN = len(rsFrames[0])
	}
	p.stats.ResampledFrames += int64(outN)
	interleaved := Interleave(rsFrames)
	normalized, err := NormalizeBlock(interleaved, p.matrix)
	if err != nil {
		return nil, err
	}
	limited, loud, err := LimitLoudness(normalized, p.outSpec.Channels, p.OutLayout, p.OutRate, p.TargetLUFS)
	if err != nil {
		return nil, err
	}
	p.stats.ClippedSamples += int64(loud.ClippedSamples)
	p.stats.OutputFrames += int64(outN)
	p.lastLoud = loud
	outStart := p.resampler.Produced() - int64(outN)
	return &BlockResult{
		Samples: limited, Frames: outN, InputFrames: inFrames, Loudness: loud,
		Interval: timeline.Interval{Start: outStart, End: outStart + int64(outN)},
	}, nil
}

// Flush drains the resampler tail with the fixed zero-padding policy and
// runs the remaining stages on the residual frames.
func (p *Pipeline) Flush() (*BlockResult, error) {
	rsFrames := p.resampler.Flush()
	outN := 0
	if len(rsFrames) > 0 {
		outN = len(rsFrames[0])
	}
	if outN == 0 {
		return &BlockResult{}, nil
	}
	p.stats.ResampledFrames += int64(outN)
	normalized, err := NormalizeBlock(Interleave(rsFrames), p.matrix)
	if err != nil {
		return nil, err
	}
	limited, loud, err := LimitLoudness(normalized, p.outSpec.Channels, p.OutLayout, p.OutRate, p.TargetLUFS)
	if err != nil {
		return nil, err
	}
	p.stats.ClippedSamples += int64(loud.ClippedSamples)
	p.stats.OutputFrames += int64(outN)
	p.lastLoud = loud
	outStart := p.resampler.Produced() - int64(outN)
	return &BlockResult{
		Samples: limited, Frames: outN, Loudness: loud,
		Interval: timeline.Interval{Start: outStart, End: p.resampler.Produced()},
	}, nil
}

// Stats returns a copy of the accumulated cross-stage counters.
func (p *Pipeline) Stats() StageStats { return p.stats }

// LastLoudness returns the most recent loudness-stage report.
func (p *Pipeline) LastLoudness() LoudnessReport { return p.lastLoud }

// ExpectedOutputFrames is the theoretical total output for n input frames.
func (p *Pipeline) ExpectedOutputFrames(inputFrames int64) int64 {
	return p.resampler.ExpectedOutputs(inputFrames)
}
