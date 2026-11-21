package loudness

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Channel weights follow BS.1770-4: every non-LFE channel counts at full
// weight except the surround channels, which use 1.41 (+1.5 dB).
const surroundWeight = 1.41

// Layout maps WAV channels to loudness weights.
type Layout struct {
	Name     string
	Channels int
	Weights  []float64
	// Names documents the expected WAV channel order for reports.
	ChannelNames []string
}

// Named layouts. The WAVE channel order is FL, FR, FC, LFE, BL, BR for 5.1
// and FL, FR, FC, LFE, BL, BR, SL, SR for 7.1, which is what extensible WAV
// writers emit by default.
var (
	LayoutMono   = Layout{Name: "mono", Channels: 1, Weights: []float64{1}, ChannelNames: []string{"FC"}}
	LayoutStereo = Layout{Name: "stereo", Channels: 2, Weights: []float64{1, 1}, ChannelNames: []string{"FL", "FR"}}
	Layout51     = Layout{Name: "5.1", Channels: 6,
		Weights:      []float64{1, 1, 1, 0, surroundWeight, surroundWeight},
		ChannelNames: []string{"FL", "FR", "FC", "LFE", "BL", "BR"}}
	Layout71 = Layout{Name: "7.1", Channels: 8,
		Weights:      []float64{1, 1, 1, 0, surroundWeight, surroundWeight, surroundWeight, surroundWeight},
		ChannelNames: []string{"FL", "FR", "FC", "LFE", "BL", "BR", "SL", "SR"}}
)

// NamedLayouts lists the layouts selectable by name, in report order.
var NamedLayouts = []Layout{LayoutMono, LayoutStereo, Layout51, Layout71}

// LookupLayout resolves a layout name. It returns false for unknown names.
func LookupLayout(name string) (Layout, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, l := range NamedLayouts {
		if l.Name == n {
			return l, true
		}
	}
	switch n {
	case "monaural":
		return LayoutMono, true
	case "2.0", "l/r":
		return LayoutStereo, true
	case "5.1(side)", "5.1side":
		return Layout51, true
	}
	return Layout{}, false
}

// InferLayout picks the layout matching a channel count. It fails for
// channel counts that have no unambiguous default, forcing the caller to
// supply an explicit map rather than guessing.
func InferLayout(channels int) (Layout, bool) {
	for _, l := range NamedLayouts {
		if l.Channels == channels {
			return l, true
		}
	}
	return Layout{}, false
}

// ErrLayout marks errors caused by a channel count this tool cannot map, or
// by a mapping that does not fit the file. These are input-format problems:
// no measurement can be produced until the caller supplies a usable layout.
var ErrLayout = errors.New("layout")

func layoutErrorf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrLayout, fmt.Sprintf(format, args...))
}

// ParseMapping parses an explicit weight list such as "1,1,1,0,1.41,1.41".
// The number of weights must equal wantChannels, otherwise analysis is
// refused: a mapping that does not cover every channel is a configuration
// error, not something to paper over.
func ParseMapping(spec string, wantChannels int) (Layout, error) {
	parts := strings.Split(spec, ",")
	weights := make([]float64, 0, len(parts))
	names := make([]string, 0, len(parts))
	for _, raw := range parts {
		item := strings.TrimSpace(raw)
		if item == "" {
			return Layout{}, layoutErrorf("empty weight in mapping %q", spec)
		}
		// Accept "name=weight" or a bare weight.
		if i := strings.IndexByte(item, '='); i >= 0 {
			names = append(names, strings.TrimSpace(item[:i]))
			item = strings.TrimSpace(item[i+1:])
		} else {
			names = append(names, "")
		}
		w, err := strconv.ParseFloat(item, 64)
		if err != nil {
			return Layout{}, layoutErrorf("weight %q is not a number", item)
		}
		if w < 0 || w > 10 || w != w {
			return Layout{}, layoutErrorf("weight %v out of range", w)
		}
		weights = append(weights, w)
	}
	if len(weights) != wantChannels {
		return Layout{}, layoutErrorf("mapping has %d weights but the file has %d channels",
			len(weights), wantChannels)
	}
	for i, n := range names {
		if n == "" {
			names[i] = fmt.Sprintf("CH%d", i+1)
		}
	}
	return Layout{Name: "custom", Channels: wantChannels, Weights: weights, ChannelNames: names}, nil
}

// Resolve chooses the layout for a file. An empty name means "infer from the
// channel count"; an explicit name must match the channel count of the file.
func Resolve(name string, channels int, mapping string) (Layout, error) {
	if mapping != "" {
		if name != "" && !strings.EqualFold(name, "custom") {
			return Layout{}, layoutErrorf("--map cannot be combined with --layout %s", name)
		}
		return ParseMapping(mapping, channels)
	}
	if name == "" || strings.EqualFold(name, "auto") {
		l, ok := InferLayout(channels)
		if !ok {
			return Layout{}, layoutErrorf("%d channels has no default layout; supply --map", channels)
		}
		return l, nil
	}
	if strings.EqualFold(name, "custom") {
		return Layout{}, layoutErrorf("--layout custom requires --map")
	}
	l, ok := LookupLayout(name)
	if !ok {
		return Layout{}, layoutErrorf("unknown layout %q", name)
	}
	if l.Channels != channels {
		return Layout{}, layoutErrorf("%s expects %d channels but the file has %d",
			l.Name, l.Channels, channels)
	}
	return l, nil
}
