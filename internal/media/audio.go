// Package media normalizes audio channel layouts/sample rates and video color
// metadata under explicit, deterministic project policies.
package media

import (
	"errors"
	"fmt"
)

// Channel is a logical speaker position, ordered by the canonical WAVE/MP4
// channel-mapping convention.
type Channel uint8

const (
	ChLeft Channel = iota
	ChRight
	ChCenter
	ChLFE
	ChSurroundLeft
	ChSurroundRight
)

// Names render the canonical channel tags used in reports.
var channelNames = map[Channel]string{
	ChLeft: "L", ChRight: "R", ChCenter: "C", ChLFE: "LFE",
	ChSurroundLeft: "Ls", ChSurroundRight: "Rs",
}

func (c Channel) String() string {
	if n, ok := channelNames[c]; ok {
		return n
	}
	return fmt.Sprintf("ch%d", uint8(c))
}

// Layout is an ordered list of logical channels for one PCM stream.
type Layout struct {
	Channels []Channel
}

// Common layouts used by the fixture generator.
var (
	LayoutMono   = Layout{Channels: []Channel{ChCenter}}
	LayoutStereo = Layout{Channels: []Channel{ChLeft, ChRight}}
	Layout51     = Layout{Channels: []Channel{ChLeft, ChRight, ChCenter, ChLFE, ChSurroundLeft, ChSurroundRight}}
)

// Equal reports whether two layouts have identical ordered channels.
func (l Layout) Equal(o Layout) bool {
	if len(l.Channels) != len(o.Channels) {
		return false
	}
	for i := range l.Channels {
		if l.Channels[i] != o.Channels[i] {
			return false
		}
	}
	return true
}

func (l Layout) String() string {
	out := ""
	for i, c := range l.Channels {
		if i > 0 {
			out += "."
		}
		out += c.String()
	}
	return out
}

// MappingMatrix converts PCM frames from a source layout to a target layout.
// Matrix[target][source] is the linear gain for that contribution. Every
// target channel must have at least one non-zero coefficient.
type MappingMatrix struct {
	From   Layout
	To     Layout
	Values [][]float64 // [target index][source index]
}

// Validate enforces dimensions, coverage and finite coefficients.
func (m MappingMatrix) Validate() error {
	if len(m.Values) != len(m.To.Channels) {
		return fmt.Errorf("%w: matrix rows %d != target channels %d", ErrLayoutMismatch, len(m.Values), len(m.To.Channels))
	}
	for row, v := range m.Values {
		if len(v) != len(m.From.Channels) {
			return fmt.Errorf("%w: row %d width %d != source channels %d", ErrLayoutMismatch, row, len(v), len(m.From.Channels))
		}
		any := false
		for _, g := range v {
			if g != g || g < -4 || g > 4 { // NaN check plus sane gain bound
				return fmt.Errorf("%w: illegal gain %v at target %d", ErrLayoutMismatch, g, row)
			}
			if g != 0 {
				any = true
			}
		}
		if !any {
			return fmt.Errorf("%w: target channel %d (%s) has no source contribution",
				ErrLayoutMismatch, row, m.To.Channels[row])
		}
	}
	return nil
}

// StandardMatrix returns the documented down/up-mix between supported layouts,
// or an error when no policy-defined mapping exists. LFE is never synthesized.
func StandardMatrix(from, to Layout) (MappingMatrix, error) {
	if from.Equal(to) {
		return identityMatrix(from), nil
	}
	switch {
	case from.Equal(LayoutStereo) && to.Equal(LayoutMono):
		// Equal-power-ish mono fold: 0.5 of each leg.
		return MappingMatrix{From: from, To: to, Values: [][]float64{{0.5, 0.5}}}, nil
	case from.Equal(LayoutMono) && to.Equal(LayoutStereo):
		return MappingMatrix{From: from, To: to, Values: [][]float64{{1}, {1}}}, nil
	case from.Equal(Layout51) && to.Equal(LayoutStereo):
		// Lo/Ro downmix; LFE dropped (documented, never faked into mains).
		return MappingMatrix{From: from, To: to, Values: [][]float64{
			{1, 0, 0.7071, 0, 0.7071, 0}, // L + .707(C+Ls)
			{0, 1, 0.7071, 0, 0, 0.7071}, // R + .707(C+Rs)
		}}, nil
	default:
		return MappingMatrix{}, fmt.Errorf("%w: no standard mapping %s -> %s", ErrLayoutMismatch, from, to)
	}
}

func identityMatrix(l Layout) MappingMatrix {
	v := make([][]float64, len(l.Channels))
	for i := range v {
		row := make([]float64, len(l.Channels))
		row[i] = 1
		v[i] = row
	}
	return MappingMatrix{From: l, To: l, Values: v}
}

// ErrLayoutMismatch marks rejected channel configuration.
var ErrLayoutMismatch = errors.New("channel layout mismatch")

// ApplyPCM transforms interleaved float64 frames using the matrix. Input is
// laid out as frames*len(From); output as frames*len(To). Deterministic,
// allocation-bounded, no randomness.
func (m MappingMatrix) ApplyPCM(in []float64) ([]float64, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	srcN := len(m.From.Channels)
	if len(in)%srcN != 0 {
		return nil, fmt.Errorf("%w: input sample count %d not divisible by %d channels",
			ErrLayoutMismatch, len(in), srcN)
	}
	frames := len(in) / srcN
	out := make([]float64, frames*len(m.To.Channels))
	for f := 0; f < frames; f++ {
		base := f * srcN
		for t := range m.To.Channels {
			var acc float64
			for s := 0; s < srcN; s++ {
				acc += m.Values[t][s] * in[base+s]
			}
			out[f*len(m.To.Channels)+t] = acc
		}
	}
	return out, nil
}
