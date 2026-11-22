// Package media normalizes audio channel layouts, sample rates and color
// metadata into deterministic fragment results carrying the shared
// timeline error model. Nothing is ever dropped silently.
package media

import (
	"sort"

	"mediadist/timeline"
)

// Limits bound per-fragment metadata so malformed inputs stay bounded.
const (
	MaxChannels        = 24
	MaxFragmentPayload = 1 << 20
)

// AudioMeta describes one source fragment's audio properties.
type AudioMeta struct {
	Source     string
	Channels   int
	SampleRate uint32
	Layout     []string
}

// FragmentResult is the normalized output handed to the timeline engine.
type FragmentResult struct {
	Source       string
	TargetRate   uint32
	Channels     int
	Layout       []string
	Color        ColorMeta
	RateScale    timeline.Rat
	Error        *timeline.Error
	MetadataHash string
}

// Failed reports whether normalization produced a coded error.
func (r FragmentResult) Failed() bool { return r.Error != nil }

// AudioNormalizer maps incoming audio onto one canonical sample rate
// and channel layout, using exact rational math.
type AudioNormalizer struct {
	TargetRate uint32
	allowed    map[uint32]bool
}

// CanonicalLayout is the deterministic channel label set for a count.
var CanonicalLayout = map[int][]string{
	1: {"mono"},
	2: {"left", "right"},
	6: {"left", "right", "center", "lfe", "surround_left", "surround_right"},
	8: {"left", "right", "center", "lfe", "back_left", "back_right", "side_left", "side_right"},
}

// NewAudioNormalizer requires a positive target rate.
func NewAudioNormalizer(targetRate uint32) (*AudioNormalizer, error) {
	if targetRate == 0 {
		return nil, timeline.NewError("INVALID_TIMESCALE",
			"target sample rate must be positive", nil)
	}
	return &AudioNormalizer{
		TargetRate: targetRate,
		allowed: map[uint32]bool{
			8000: true, 16000: true, 24000: true, 44100: true,
			48000: true, 96000: true,
		},
	}, nil
}

// Normalize validates and converts one fragment. Layouts that cannot be
// mapped fail explicitly with UNSUPPORTED_CHANNEL_LAYOUT.
func (n *AudioNormalizer) Normalize(m AudioMeta) (FragmentResult, error) {
	if m.Channels <= 0 || m.Channels > MaxChannels {
		return FragmentResult{Source: m.Source}, timeline.NewError(
			"UNSUPPORTED_CHANNEL_LAYOUT", "channel count out of range",
			map[string]any{"source": m.Source, "channels": m.Channels})
	}
	if !n.allowed[m.SampleRate] {
		return FragmentResult{Source: m.Source}, timeline.NewError(
			"INVALID_TIMESCALE", "source sample rate unsupported",
			map[string]any{"source": m.Source, "rate": m.SampleRate})
	}
	canon, ok := CanonicalLayout[m.Channels]
	if !ok {
		return FragmentResult{Source: m.Source}, timeline.NewError(
			"UNSUPPORTED_CHANNEL_LAYOUT", "no canonical mapping for channel count",
			map[string]any{"source": m.Source, "channels": m.Channels})
	}
	if len(m.Layout) != 0 && !sameSet(m.Layout, canon) {
		return FragmentResult{Source: m.Source}, timeline.NewError(
			"UNSUPPORTED_CHANNEL_LAYOUT", "channel labels do not match canonical layout",
			map[string]any{"source": m.Source, "layout": copyStrings(m.Layout)})
	}
	scale, err := timeline.NewRat(int64(n.TargetRate), int64(m.SampleRate))
	if err != nil {
		return FragmentResult{Source: m.Source}, err
	}
	return FragmentResult{
		Source:     m.Source,
		TargetRate: n.TargetRate,
		Channels:   m.Channels,
		Layout:     copyStrings(canon),
		RateScale:  scale,
	}, nil
}

// ResampleTicks maps an exact sample count from sourceRate to the
// canonical rate. Non-exact conversion is rejected rather than rounded.
func (n *AudioNormalizer) ResampleTicks(sourceTicks uint64, sourceRate uint32) (uint64, error) {
	if sourceRate == 0 {
		return 0, timeline.NewError("INVALID_TIMESCALE",
			"source sample rate must be positive", nil)
	}
	r, err := timeline.NewRat(int64(sourceTicks)*int64(n.TargetRate), int64(sourceRate))
	if err != nil {
		return 0, err
	}
	ticks, err := r.ScaleTo(1)
	if err != nil {
		return 0, timeline.NewError("NON_EXACT_SCALE",
			"sample count does not convert exactly to canonical rate",
			map[string]any{
				"ticks": sourceTicks, "source_rate": sourceRate,
				"target_rate": n.TargetRate,
			})
	}
	return uint64(ticks), nil
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := copyStrings(a), copyStrings(b)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func copyStrings(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}
