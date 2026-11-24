// Package audio implements the deterministic audio processing stages:
// channel-layout normalization, sample-rate conversion and loudness control.
package audio

import (
	"fmt"

	"mediapipe/internal/model"
)

// Sample is one linear PCM sample in the [-1, 1] float range.
type Sample float64

// Frame holds one sample per channel at a single time instant.
type Frame []Sample

// Matrix maps inCh input channels onto 2 output channels (L, R). Rows are
// outputs, columns inputs: Out[o] = sum_m Matrix[o][m] * In[m].
type Matrix struct {
	InCh   int
	Name   string
	Coeffs [2][]float64
}

// Apply mixes one input frame into a stereo Frame.
func (m Matrix) Apply(in Frame) (Frame, error) {
	if len(in) != m.InCh {
		return nil, model.NewError(model.ErrInvalidConfig, "", 0, 0,
			fmt.Sprintf("layout %s expects %d channels, got %d", m.Name, m.InCh, len(in)))
	}
	out := make(Frame, 2)
	for c := 0; c < 2; c++ {
		var s Sample
		for i, g := range m.Coeffs[c] {
			s += Sample(g) * in[i]
		}
		out[c] = s
	}
	return out, nil
}

// Standard downmix coefficients (ITU/EBU-style constant-power 5.1 fold-down):
// L/R pass through, center is shared, surrounds are attenuated by ~3 dB.
var (
	MonoMatrix = Matrix{
		InCh:   1,
		Name:   "mono->stereo",
		Coeffs: [2][]float64{{1}, {1}},
	}
	StereoMatrix = Matrix{
		InCh:   2,
		Name:   "stereo passthrough",
		Coeffs: [2][]float64{{1, 0}, {0, 1}},
	}
	FiveOneMatrix = Matrix{
		InCh: 6,
		Name: "5.1->stereo",
		// Input order: L, R, C, LFE, Ls, Rs.
		Coeffs: [2][]float64{
			{1, 0, 0.7071, 0, 0.7071, 0},
			{0, 1, 0.7071, 0, 0, 0.7071},
		},
	}
)

// MatrixFor resolves the explicit matrix for a supported channel count.
func MatrixFor(channels int) (Matrix, error) {
	switch channels {
	case 1:
		return MonoMatrix, nil
	case 2:
		return StereoMatrix, nil
	case 6:
		return FiveOneMatrix, nil
	default:
		return Matrix{}, model.NewError(model.ErrInvalidConfig, "", 0, 0,
			fmt.Sprintf("unsupported channel count %d (want 1, 2 or 6)", channels))
	}
}

// Normalize converts a whole interleaved PCM buffer (samples grouped by
// frame) into interleaved stereo. The LFE channel is intentionally dropped
// per the declared matrix.
func Normalize(pcm []float64, inCh int) ([]float64, string, error) {
	mtx, err := MatrixFor(inCh)
	if err != nil {
		return nil, "", err
	}
	if len(pcm)%inCh != 0 {
		return nil, "", model.NewError(model.ErrInvalidConfig, "", 0, 0,
			fmt.Sprintf("PCM length %d not divisible by %d channels", len(pcm), inCh))
	}
	out := make([]float64, 0, (len(pcm)/inCh)*2)
	in := make(Frame, inCh)
	for i := 0; i < len(pcm); i += inCh {
		for j := range in {
			in[j] = Sample(pcm[i+j])
		}
		st, err := mtx.Apply(in)
		if err != nil {
			return nil, "", err
		}
		out = append(out, float64(st[0]), float64(st[1]))
	}
	return out, mtx.Name, nil
}
