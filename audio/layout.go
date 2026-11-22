package audio

import (
	"fmt"

	"medpipe/timeline"
)

// Layout names accepted by the normalizer. Every name maps to an explicit
// channel count and ordering; no channel is ever dropped implicitly.
const (
	LayoutMono      = "mono"
	LayoutStereo    = "stereo"
	Layout51        = "5.1"      // L R C LFE Ls Rs
	Layout71        = "7.1"      // L R C LFE Ls Rs Rls Rrs
	LayoutStereoAlt = "stereo2"  // L R with swapped source order fixture
)

// LayoutSpec fixes the number and semantic order of channels in a layout.
type LayoutSpec struct {
	Name     string
	Channels int
	Labels   []string
}

var layoutRegistry = map[string]LayoutSpec{
	LayoutMono:      {LayoutMono, 1, []string{"C"}},
	LayoutStereo:    {LayoutStereo, 2, []string{"L", "R"}},
	LayoutStereoAlt: {LayoutStereoAlt, 2, []string{"R", "L"}},
	Layout51:        {Layout51, 6, []string{"L", "R", "C", "LFE", "Ls", "Rs"}},
	Layout71:        {Layout71, 8, []string{"L", "R", "C", "LFE", "Ls", "Rs", "Rls", "Rrs"}},
}

// LookupLayout returns the spec for a registered layout. Unknown names are
// rejected so channels can never be silently discarded.
func LookupLayout(name string) (LayoutSpec, error) {
	spec, ok := layoutRegistry[name]
	if !ok {
		return LayoutSpec{}, timeline.NewError(timeline.ErrCodeUnknownLayout, timeline.StageLayout, "",
			"unknown channel layout %q", name)
	}
	return spec, nil
}

// Matrix is an explicit output[channel] x input[channel] gain matrix.
type Matrix struct {
	From   string
	To     string
	Gains  [][]float64 // rows: output channels, cols: input channels
}

// Apply maps one interleaved frame of input samples to the target layout.
func (m *Matrix) Apply(frame []float64) ([]float64, error) {
	if len(frame) != len(m.Gains[0]) {
		return nil, timeline.NewError(timeline.ErrCodeIllegalBlockLength, timeline.StageLayout, "",
			"frame has %d channels, matrix %s->%s expects %d",
			len(frame), m.From, m.To, len(m.Gains[0]))
	}
	out := make([]float64, len(m.Gains))
	for o := range m.Gains {
		var acc float64
		for i, g := range m.Gains[o] {
			acc += g * frame[i]
		}
		out[o] = acc
	}
	return out, nil
}

// BuildMatrix constructs the explicit normalization matrix between two known
// layouts. Every input channel has a defined contribution to at least one
// output channel, which is asserted before the matrix is returned.
func BuildMatrix(from, to string) (*Matrix, error) {
	src, err := LookupLayout(from)
	if err != nil {
		return nil, err
	}
	dst, err := LookupLayout(to)
	if err != nil {
		return nil, err
	}
	g := make([][]float64, dst.Channels)
	for i := range g {
		g[i] = make([]float64, src.Channels)
	}
	switch {
	case from == to:
		for i := 0; i < src.Channels; i++ {
			g[i][i] = 1
		}
	case from == LayoutMono && to == LayoutStereo:
		g[0][0], g[1][0] = 1, 1
	case from == LayoutStereo && to == LayoutMono:
		g[0][0], g[0][1] = 0.5, 0.5
	case from == LayoutStereoAlt && to == LayoutStereo:
		// source order is R,L: swap explicitly
		g[0][1], g[1][0] = 1, 1
	case src.Channels > 2 && dst.Channels == 2:
		// Surround fold-down: L/R and center/surround contributions are
		// explicit per input channel; LFE is attenuated, never dropped.
		index := func(label string) int {
			for i, l := range src.Labels {
				if l == label {
					return i
				}
			}
			return -1
		}
		l, r, c := index("L"), index("R"), index("C")
		ls, rs, lfe := index("Ls"), index("Rs"), index("LFE")
		rls, rrs := index("Rls"), index("Rrs")
		put := func(out, in int, gain float64) {
			if in >= 0 {
				g[out][in] += gain
			}
		}
		put(0, l, 1)
		put(1, r, 1)
		put(0, c, 0.7071)
		put(1, c, 0.7071)
		put(0, ls, 0.7071)
		put(1, rs, 0.7071)
		put(0, rls, 0.5)
		put(1, rrs, 0.5)
		put(0, lfe, 0.25)
		put(1, lfe, 0.25)
	case src.Channels == 1 && dst.Channels > 2:
		// mono into a multichannel layout: feed the center channel only
		idx := indexOf(dst.Labels, "C")
		g[idx][0] = 1
	default:
		return nil, timeline.NewError(timeline.ErrCodeUnknownLayout, timeline.StageLayout, "",
			"no explicit normalization defined from %q to %q", from, to)
	}
	// Invariant: every input column carries at least one non-zero gain.
	for in := 0; in < src.Channels; in++ {
		defined := false
		for o := 0; o < dst.Channels; o++ {
			if g[o][in] != 0 {
				defined = true
			}
		}
		if !defined {
			return nil, timeline.NewError(timeline.ErrCodeUnknownLayout, timeline.StageLayout, "",
				"layout %s->%s would drop input channel %d (%s)",
				from, to, in, src.Labels[in])
		}
	}
	return &Matrix{From: from, To: to, Gains: g}, nil
}

func indexOf(labels []string, want string) int {
	for i, l := range labels {
		if l == want {
			return i
		}
	}
	return -1
}

// NormalizeBlock applies a matrix to every interleaved frame of a block.
func NormalizeBlock(samples []float64, m *Matrix) ([]float64, error) {
	inCh := len(m.Gains[0])
	if len(samples)%inCh != 0 {
		return nil, timeline.NewError(timeline.ErrCodeIllegalBlockLength, timeline.StageLayout, "",
			"block of %d samples does not tile %d input channels", len(samples), inCh)
	}
	frames := len(samples) / inCh
	outCh := len(m.Gains)
	out := make([]float64, frames*outCh)
	for f := 0; f < frames; f++ {
		mapped, err := m.Apply(samples[f*inCh : (f+1)*inCh])
		if err != nil {
			return nil, err
		}
		copy(out[f*outCh:(f+1)*outCh], mapped)
	}
	return out, nil
}

// String renders the matrix for deterministic manifest output.
func (m *Matrix) String() string {
	return fmt.Sprintf("matrix[%s->%s]", m.From, m.To)
}
