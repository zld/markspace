package audio

import (
	"hash/fnv"
	"math"
)

// LoudnessConfig parameterizes loudness normalization.
type LoudnessConfig struct {
	// TargetLUFS is the integrated loudness target; practical range -32..-8.
	TargetLUFS float64
	// MaxGain caps upward gain so near-silent input is not exploded.
	MaxGain float64
	// TruePeak is the hard ceiling in dBFS; samples beyond it are limited.
	TruePeakDB float64
}

// DefaultLoudness is used when the caller supplies no config.
var DefaultLoudness = LoudnessConfig{TargetLUFS: -16, MaxGain: 12, TruePeakDB: -1}

// LoudnessStats reports the measured and applied loudness values.
type LoudnessStats struct {
	InputLUFS  float64 `json:"input_lufs"`
	OutputLUFS float64 `json:"output_lufs"`
	GainDB     float64 `json:"gain_db"`
	GainLinear float64 `json:"gain_linear"`
	Limited    int     `json:"samples_limited"`
}

// AudioStats is the deterministic summary of one full pipeline run.
type AudioStats struct {
	InputRate      int           `json:"input_rate"`
	InputChannels  int           `json:"input_channels"`
	OutputRate     int           `json:"output_rate"`
	OutputChannels int           `json:"output_channels"`
	Periods        int           `json:"output_periods"`
	PeakAbs        int16         `json:"peak_abs"`
	RMS            float64       `json:"rms"`
	Loudness       LoudnessStats `json:"loudness"`
	// Checksum hashes the exact output bytes; repeated runs must match.
	Checksum uint64 `json:"checksum"`
}

// PipelineConfig is the complete audio transformation parameter set.
type PipelineConfig struct {
	TargetRate int
	Loudness   LoudnessConfig
}

// Process runs the fixed pipeline order: layout -> resample -> loudness.
func Process(in Frame, cfg PipelineConfig) (Frame, AudioStats, error) {
	normalized, layoutSt, err := NormalizeLayout(in)
	if err != nil {
		return Frame{}, AudioStats{}, err
	}
	rate := cfg.TargetRate
	if rate == 0 {
		rate = normalized.SampleRate
	}
	resampled, rsSt, err := Resample(normalized, rate)
	if err != nil {
		return Frame{}, AudioStats{}, err
	}
	loud := cfg.Loudness
	if loud == (LoudnessConfig{}) {
		loud = DefaultLoudness
	}
	out, loudSt := ApplyLoudness(resampled, loud)

	st := AudioStats{
		InputRate: in.SampleRate, InputChannels: layoutSt.InputChannels,
		OutputRate: rsSt.OutputRate, OutputChannels: CanonicalChannels,
		Periods:  rsSt.OutputPeriods,
		PeakAbs:  peakAbs(out.Samples),
		RMS:      rms(out.Samples),
		Loudness: loudSt,
		Checksum: checksum(out.Samples),
	}
	return out, st, nil
}

// ApplyLoudness measures integrated RMS loudness, computes the linear gain
// toward the LUFS target (capped at MaxGain dB), applies it with half-away
// rounding and enforces the true-peak ceiling by hard limiting.
func ApplyLoudness(in Frame, cfg LoudnessConfig) (Frame, LoudnessStats) {
	inputLUFS := measureLUFS(in.Samples)
	gainDB := cfg.TargetLUFS - inputLUFS
	if gainDB > cfg.MaxGain {
		gainDB = cfg.MaxGain
	}
	if gainDB < -cfg.MaxGain {
		gainDB = -cfg.MaxGain
	}
	gain := math.Pow(10, gainDB/20)

	ceiling := int64(math.Round(32767 * math.Pow(10, cfg.TruePeakDB/20)))
	out := Frame{SampleRate: in.SampleRate, Channels: in.Channels,
		Samples: make([]int16, len(in.Samples))}
	limited := 0
	for i, s := range in.Samples {
		scaled := roundHalfAway(float64(s) * gain)
		if scaled > ceiling {
			scaled = ceiling
			limited++
		} else if scaled < -ceiling {
			scaled = -ceiling
			limited++
		}
		out.Samples[i] = int16(scaled)
	}
	return out, LoudnessStats{
		InputLUFS: round2(inputLUFS), OutputLUFS: round2(measureLUFS(out.Samples)),
		GainDB: round2(gainDB), GainLinear: round6(gain), Limited: limited,
	}
}

// measureLUFS approximates integrated loudness from full-scale RMS. s16 full
// scale (32768) is 0 dBFS; silence is reported as -120 LUFS, a conventional
// "digital zero" that keeps every output finite and deterministic.
func measureLUFS(samples []int16) float64 {
	if len(samples) == 0 {
		return -120
	}
	r := rms(samples)
	if r == 0 {
		return -120
	}
	return 20 * math.Log10(r/32768)
}

func rms(samples []int16) float64 {
	var sum float64
	for _, s := range samples {
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(len(samples)))
}

func peakAbs(samples []int16) int16 {
	var peak int16
	for _, s := range samples {
		v := s
		if v < 0 {
			v = int16(-int32(s))
		}
		if v > peak {
			peak = v
		}
	}
	return peak
}

func roundHalfAway(v float64) int64 {
	if v >= 0 {
		return int64(math.Floor(v + 0.5))
	}
	return -int64(math.Floor(-v + 0.5))
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// checksum hashes the output sample bytes with FNV-1a. Floats never enter the
// hash, so the value is stable across processes and platforms.
func checksum(samples []int16) uint64 {
	h := fnv.New64a()
	var buf [2]byte
	for _, s := range samples {
		buf[0] = byte(s)
		buf[1] = byte(s >> 8)
		_, _ = h.Write(buf[:])
	}
	return h.Sum64()
}
