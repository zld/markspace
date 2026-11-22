package audio

import (
	"fmt"

	"mediatimeline/internal/clock"
)

// ChannelPolicy decides how layout mismatches are handled.
type ChannelPolicy int

const (
	// PolicyError fails the stage and records a CHANNEL_POLICY error.
	PolicyError ChannelPolicy = iota
	// PolicyZeroFill substitutes silence for missing channels and drops
	// surplus channels, while recording the violation in Errors().
	PolicyZeroFill
	// PolicyDownmix averages surplus channels into the nearest target slot and
	// zero-fills missing ones.
	PolicyDownmix
)

// Layout is an explicit ordered channel map. Labels follow WAVE_FORMAT
// extensible conventions: "L","R","C","LFE","Ls","Rs",...
type Layout struct {
	Labels []string
}

// Common layouts used by the fixtures and demo.
var (
	LayoutMono   = Layout{Labels: []string{"C"}}
	LayoutStereo = Layout{Labels: []string{"L", "R"}}
	Layout51     = Layout{Labels: []string{"L", "R", "C", "LFE", "Ls", "Rs"}}
)

// LayoutError records a per-sample mapping violation for the error report.
type LayoutError struct {
	Code   clock.ErrorCode
	Detail string
}

// Mapper converts interleaved frames from a source layout to a target layout
// using an explicit per-target source-index map. -1 means the target channel
// has no source (missing). Multiple targets may reference one source.
type Mapper struct {
	from, to     Layout
	policy       ChannelPolicy
	sourceIndex  []int // target channel -> source index, -1 if missing
	extraSources []int // source channels consumed by no target
	errors       []LayoutError
	mappedFrames uint64
}

// BuildMapper constructs the explicit mapping. It never guesses: channels are
// matched by label, and every mismatch is resolved through the policy.
func BuildMapper(from, to Layout, policy ChannelPolicy) (*Mapper, error) {
	if len(from.Labels) == 0 || len(to.Labels) == 0 {
		return nil, clock.NewError(clock.ErrChannelPolicy, "empty channel layout")
	}
	srcPos := make(map[string]int, len(from.Labels))
	for i, l := range from.Labels {
		srcPos[l] = i
	}
	m := &Mapper{from: from, to: to, policy: policy, sourceIndex: make([]int, len(to.Labels))}
	used := make([]bool, len(from.Labels))
	for ti, label := range to.Labels {
		if si, ok := srcPos[label]; ok {
			m.sourceIndex[ti] = si
			used[si] = true
		} else {
			m.sourceIndex[ti] = -1
		}
	}
	for si, u := range used {
		if !u {
			m.extraSources = append(m.extraSources, si)
		}
	}
	if len(m.extraSources) > 0 || hasMissing(m.sourceIndex) {
		if policy == PolicyError {
			return nil, clock.NewError(clock.ErrChannelPolicy, fmt.Sprintf(
				"layout %v -> %v: missing=%v extra=%v",
				from.Labels, to.Labels, missingLabels(to, m.sourceIndex),
				extraLabels(from, m.extraSources)))
		}
		m.errors = append(m.errors, LayoutError{
			Code:   clock.ErrChannelPolicy,
			Detail: fmt.Sprintf("missing=%v extra=%v policy=%d", m.sourceIndex, m.extraSources, policy),
		})
	}
	return m, nil
}

func hasMissing(idx []int) bool {
	for _, v := range idx {
		if v < 0 {
			return true
		}
	}
	return false
}

func missingLabels(to Layout, idx []int) []string {
	var out []string
	for i, v := range idx {
		if v < 0 {
			out = append(out, to.Labels[i])
		}
	}
	return out
}

func extraLabels(from Layout, idx []int) []string {
	out := make([]string, len(idx))
	for i, v := range idx {
		out[i] = from.Labels[v]
	}
	return out
}

// Apply converts one interleaved block to the target channel count.
func (m *Mapper) Apply(in []float64) []float64 {
	inFrames := len(in) / len(m.from.Labels)
	outW := len(m.to.Labels)
	out := make([]float64, inFrames*outW)
	for f := 0; f < inFrames; f++ {
		for t := 0; t < outW; t++ {
			si := m.sourceIndex[t]
			switch {
			case si >= 0:
				out[f*outW+t] = in[f*len(m.from.Labels)+si]
			case m.policy == PolicyDownmix && len(m.extraSources) > 0:
				var sum float64
				for _, ex := range m.extraSources {
					sum += in[f*len(m.from.Labels)+ex]
				}
				out[f*outW+t] = sum / float64(len(m.extraSources))
			default: // PolicyZeroFill, or downmix with nothing to mix
				out[f*outW+t] = 0
			}
		}
	}
	m.mappedFrames += uint64(inFrames)
	return out
}

// Errors returns the mapping violations recorded so far.
func (m *Mapper) Errors() []LayoutError { return m.errors }

// MappedFrames reports cumulative frames through the mapper.
func (m *Mapper) MappedFrames() uint64 { return m.mappedFrames }
