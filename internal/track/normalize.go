package track

import (
	"fmt"

	"rtptrace/internal/manifest"
	"rtptrace/internal/timeline"
)

// NormalizationError is anchored to a concrete track and never guesses.
type NormalizationError struct {
	TrackID uint32 `json:"track_id"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *NormalizationError) Error() string {
	return fmt.Sprintf("track %d %s: %s", e.TrackID, e.Code, e.Message)
}

// AudioPlan records the explicit channel matrix and exact resampling ratio.
type AudioPlan struct {
	TrackID           uint32                `json:"track_id"`
	SourceLayout      string                `json:"source_layout"`
	TargetLayout      string                `json:"target_layout"`
	SourceChannels    int                   `json:"source_channels"`
	TargetChannels    int                   `json:"target_channels"`
	Matrix            [][]timeline.Rational `json:"matrix"`
	SourceSampleRate  int                   `json:"source_sample_rate"`
	TargetSampleRate  int                   `json:"target_sample_rate"`
	ResampleRatio     timeline.Rational     `json:"resample_ratio"`
	ResampleAlgorithm string                `json:"resample_algorithm"`
}

var supportedRates = map[int]bool{8000: true, 16000: true, 24000: true, 44100: true, 48000: true}

// NormalizeAudio validates an audio track and returns its deterministic plan.
// Channel conversion is only performed through an explicitly known matrix.
func NormalizeAudio(t manifest.Track, targetLayout string, targetRate int) (*AudioPlan, error) {
	if t.Kind != manifest.TrackAudio || t.Audio == nil {
		return nil, nil
	}
	a := t.Audio
	if !supportedRates[a.SampleRate] || !supportedRates[targetRate] {
		return nil, &NormalizationError{t.ID, "unsupported-sample-rate",
			fmt.Sprintf("rate %d -> %d is not in the deterministic support set", a.SampleRate, targetRate)}
	}
	matrix, targetChannels, err := layoutMatrix(a.Layout, a.Channels, targetLayout)
	if err != nil {
		return nil, &NormalizationError{t.ID, "unsupported-layout", err.Error()}
	}
	return &AudioPlan{
		TrackID: t.ID, SourceLayout: a.Layout, TargetLayout: targetLayout,
		SourceChannels: a.Channels, TargetChannels: targetChannels, Matrix: matrix,
		SourceSampleRate: a.SampleRate, TargetSampleRate: targetRate,
		ResampleRatio:     timeline.Rat(int64(targetRate), int64(a.SampleRate)),
		ResampleAlgorithm: "exact-rational-linear-interpolation",
	}, nil
}

func rat(n, d int64) timeline.Rational { return timeline.Rat(n, d) }

func layoutMatrix(source string, sourceChannels int, target string) ([][]timeline.Rational, int, error) {
	layoutChannels := map[string]int{"mono": 1, "stereo": 2, "5.1": 6}
	wantSource, ok1 := layoutChannels[source]
	wantTarget, ok2 := layoutChannels[target]
	if !ok1 || !ok2 {
		return nil, 0, fmt.Errorf("layout %q -> %q has no explicit matrix", source, target)
	}
	if wantSource != sourceChannels {
		return nil, 0, fmt.Errorf("layout %q requires %d channels, got %d", source, wantSource, sourceChannels)
	}
	if source == target {
		m := make([][]timeline.Rational, wantTarget)
		for i := range m {
			m[i] = make([]timeline.Rational, sourceChannels)
			m[i][i] = rat(1, 1)
		}
		return m, wantTarget, nil
	}
	switch source + "->" + target {
	case "mono->stereo":
		return [][]timeline.Rational{{rat(1, 1)}, {rat(1, 1)}}, 2, nil
	case "stereo->mono":
		return [][]timeline.Rational{{rat(1, 2), rat(1, 2)}}, 1, nil
	case "5.1->stereo":
		// Channel order: L R C LFE Ls Rs. LFE is intentionally omitted.
		return [][]timeline.Rational{
			{rat(1, 1), rat(0, 1), rat(1, 2), rat(0, 1), rat(1, 4), rat(0, 1)},
			{rat(0, 1), rat(1, 1), rat(1, 2), rat(0, 1), rat(0, 1), rat(1, 4)},
		}, 2, nil
	default:
		return nil, 0, fmt.Errorf("layout %q -> %q has no explicit matrix", source, target)
	}
}

// ResampleI16 applies exact rational-position linear interpolation to one
// channel. It is deterministic and does not depend on wall-clock state.
func ResampleI16(in []int16, inRate, outRate int) []int16 {
	if len(in) == 0 || inRate == outRate {
		return append([]int16(nil), in...)
	}
	lastOut := (len(in)-1)*outRate/inRate + 1
	out := make([]int16, 0, lastOut)
	for i := 0; i < lastOut; i++ {
		posNum := int64(i) * int64(inRate)
		posDen := int64(outRate)
		idx := int(posNum / posDen)
		rem := posNum % posDen
		if idx >= len(in)-1 {
			out = append(out, in[len(in)-1])
			continue
		}
		a := int64(in[idx])
		b := int64(in[idx+1])
		v := a + ((b-a)*rem)/posDen
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		out = append(out, int16(v))
	}
	return out
}

// ColorDecision preserves source labels and explains any selected value.
type ColorDecision struct {
	TrackID  uint32                  `json:"track_id"`
	Source   *manifest.ColorMetadata `json:"source"`
	Selected manifest.ColorMetadata  `json:"selected"`
	Basis    string                  `json:"basis"`
	Conflict bool                    `json:"conflict"`
}

// NormalizeColor never invents or silently rewrites missing color labels.
func NormalizeColor(t manifest.Track) ColorDecision {
	d := ColorDecision{TrackID: t.ID}
	if t.Color == nil {
		d.Basis = "source-absent-preserved-as-empty"
		d.Selected = manifest.ColorMetadata{}
		return d
	}
	src := *t.Color
	d.Source = &src
	d.Conflict = src.Conflict || len(src.Alternatives) > 0
	d.Selected = manifest.ColorMetadata{
		Primaries: src.Primaries, Transfer: src.Transfer, Matrix: src.Matrix,
		Source: src.Source, Alternatives: append([]string(nil), src.Alternatives...),
		Conflict: d.Conflict,
	}
	if d.Conflict {
		d.Basis = "conflict-preserved; source-container fields retained, no rewrite"
	} else if src.Primaries == "" && src.Transfer == "" && src.Matrix == "" {
		d.Basis = "source-empty-preserved"
	} else {
		d.Basis = "source-consistent-labels-retained"
	}
	return d
}
