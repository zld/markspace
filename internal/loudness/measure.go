// Package loudness implements BS.1770-style integrated and short-term
// loudness measurement with true-peak detection.
package loudness

import (
	"math"

	"loudcheck/internal/dsp"
)

// Window geometry from ITU-R BS.1770-4.
const (
	BlockMS     = 400
	HopMS       = 100
	ShortTermMS = 3000
	// loudnessOffset calibrates a full-scale 997 Hz sine to -3.01 LUFS.
	loudnessOffset = -0.691
	absoluteGate   = -70.0 // LUFS
)

// Result is the outcome of analysing one file.
type Result struct {
	// IntegratedLUFS is the gated integrated loudness. It is only meaningful
	// when IntegratedValid is true.
	IntegratedLUFS  float64
	IntegratedValid bool
	// GatedBlockCount is the number of 400 ms blocks that survived both
	// gates, and TotalBlockCount the number of blocks formed at all.
	GatedBlockCount int
	TotalBlockCount int

	// ShortTermPeakLUFS is the loudest 3 s window. When the material is
	// shorter than 3 s ShortTermInsufficient is set and the value is unused.
	ShortTermPeakLUFS     float64
	ShortTermValid        bool
	ShortTermInsufficient bool

	// TruePeak and SamplePeak are linear amplitudes (1.0 == 0 dBFS).
	TruePeak   float64
	SamplePeak float64

	// Hops is the number of complete 100 ms hops that were analysed.
	Hops int
}

// Analyzer accumulates loudness over a stream of interleaved frames.
//
// One Analyzer measures exactly one file. Filters, true-peak history and hop
// accumulators all start empty, so no state can leak between files.
type Analyzer struct {
	sampleRate int
	channels   int
	weights    []float64

	filters   []*dsp.Filter
	truePeaks []*dsp.TruePeak
	scratch   [][]float64

	hopSamples int
	hopCount   int
	hopAccum   float64
	hopEnergy  []float64
}

// NewAnalyzer creates an analyzer for the given sample rate and per-channel
// loudness weights. A weight of zero excludes a channel (for example LFE).
func NewAnalyzer(sampleRate int, weights []float64) *Analyzer {
	ch := len(weights)
	a := &Analyzer{
		sampleRate: sampleRate,
		channels:   ch,
		weights:    append([]float64(nil), weights...),
		hopSamples: int(math.Round(float64(sampleRate) * HopMS / 1000.0)),
	}
	a.filters = make([]*dsp.Filter, ch)
	a.truePeaks = make([]*dsp.TruePeak, ch)
	a.scratch = make([][]float64, ch)
	for c := 0; c < ch; c++ {
		a.filters[c] = dsp.NewKFilter(sampleRate)
		a.truePeaks[c] = dsp.NewTruePeak()
		a.scratch[c] = make([]float64, 0, 4096)
	}
	if a.hopSamples < 1 {
		a.hopSamples = 1
	}
	return a
}

// Channels reports the channel count the analyzer was built for.
func (a *Analyzer) Channels() int { return a.channels }

// Write consumes interleaved frames. A trailing partial frame is ignored.
func (a *Analyzer) Write(interleaved []float32) {
	if len(interleaved) == 0 {
		return
	}
	frames := len(interleaved) / a.channels
	if frames == 0 {
		return
	}
	perCh := frames
	for c := 0; c < a.channels; c++ {
		if cap(a.scratch[c]) < perCh {
			a.scratch[c] = make([]float64, perCh)
		}
		a.scratch[c] = a.scratch[c][:perCh]
	}

	for f := 0; f < frames; f++ {
		base := f * a.channels
		for c := 0; c < a.channels; c++ {
			v := float64(interleaved[base+c])
			a.scratch[c][f] = v
			y := a.filters[c].Process(v)
			a.hopAccum += a.weights[c] * y * y
		}
		a.hopCount++
		if a.hopCount == a.hopSamples {
			a.hopEnergy = append(a.hopEnergy, a.hopAccum)
			a.hopAccum = 0
			a.hopCount = 0
		}
	}
	for c := 0; c < a.channels; c++ {
		a.truePeaks[c].Write(a.scratch[c])
	}
}

// Finish seals the measurement. A trailing partial hop is discarded rather
// than being extrapolated, so every block represents a full 400 ms of audio.
func (a *Analyzer) Finish() Result {
	for _, tp := range a.truePeaks {
		tp.Finish()
	}
	hopSamples := float64(a.hopSamples)

	res := Result{Hops: len(a.hopEnergy)}

	// 400 ms blocks: every hop after the first three yields one block.
	blocksPer := BlockMS / HopMS
	var blockPower []float64
	if len(a.hopEnergy) >= blocksPer {
		blockPower = make([]float64, 0, len(a.hopEnergy)-blocksPer+1)
		for end := blocksPer - 1; end < len(a.hopEnergy); end++ {
			var sum float64
			for i := end - blocksPer + 1; i <= end; i++ {
				sum += a.hopEnergy[i]
			}
			blockPower = append(blockPower, sum/(float64(blocksPer)*hopSamples))
		}
	}
	res.TotalBlockCount = len(blockPower)

	res.IntegratedLUFS, res.IntegratedValid, res.GatedBlockCount =
		integrated(blockPower)

	// 3 s short-term windows, stepped every hop.
	shortHops := ShortTermMS / HopMS
	if len(a.hopEnergy) >= shortHops {
		best := math.Inf(-1)
		var running float64
		for i, e := range a.hopEnergy {
			running += e
			if i >= shortHops {
				running -= a.hopEnergy[i-shortHops]
			}
			if i >= shortHops-1 {
				ms := running / (float64(shortHops) * hopSamples)
				if ms > 0 {
					if l := loudnessOffset + 10*math.Log10(ms); l > best {
						best = l
					}
				}
			}
		}
		if !math.IsInf(best, -1) {
			res.ShortTermPeakLUFS = best
			res.ShortTermValid = true
		}
	} else {
		res.ShortTermInsufficient = true
	}

	for _, tp := range a.truePeaks {
		if p := tp.Peak(); p > res.TruePeak {
			res.TruePeak = p
		}
		if p := tp.SamplePeak(); p > res.SamplePeak {
			res.SamplePeak = p
		}
	}
	return res
}

// integrated applies the absolute and relative gates described in BS.1770
// and returns the gated loudness. Blocks whose power is zero never pass the
// absolute gate, so digital silence yields an invalid measurement.
func integrated(power []float64) (float64, bool, int) {
	if len(power) == 0 {
		return 0, false, 0
	}
	absThreshold := math.Pow(10, (absoluteGate-loudnessOffset)/10)
	var sum float64
	var count int
	for _, p := range power {
		if p > absThreshold {
			sum += p
			count++
		}
	}
	if count == 0 {
		return 0, false, 0
	}
	relative := (sum / float64(count)) * 0.1 // -10 LU below the ungated mean
	gated := 0
	sum = 0
	for _, p := range power {
		if p > absThreshold && p > relative {
			sum += p
			gated++
		}
	}
	if gated == 0 {
		return 0, false, 0
	}
	return loudnessOffset + 10*math.Log10(sum/float64(gated)), true, gated
}

// DB converts a linear amplitude to decibels. A zero amplitude reports -Inf,
// which callers must render explicitly instead of inventing a floor value.
func DB(linear float64) float64 {
	if linear <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(linear)
}
