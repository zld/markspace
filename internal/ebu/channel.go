package ebu

import "fmt"

// Channel labels follow the WAVE_FORMAT_EXTENSIBLE / FFmpeg naming so probe
// output, error messages and fixtures all speak the same language.
type Channel string

const (
	ChannelFrontLeft   Channel = "FL"
	ChannelFrontRight  Channel = "FR"
	ChannelFrontCenter Channel = "FC"
	ChannelLFE         Channel = "LFE"
	ChannelBackLeft    Channel = "BL"
	ChannelBackRight   Channel = "BR"
	ChannelSideLeft    Channel = "SL"
	ChannelSideRight   Channel = "SR"
)

// weight is the BS.1770 channel weight G_i. LFE is always excluded because it
// is neither measured nor weighted in R128.
func weight(c Channel) float64 {
	switch c {
	case ChannelFrontLeft, ChannelFrontRight, ChannelFrontCenter:
		return 1.0
	case ChannelBackLeft, ChannelBackRight, ChannelSideLeft, ChannelSideRight:
		return 1.41
	default:
		return 0.0
	}
}

// Layout is an ordered list of channel labels. The order is the interleave
// order of the decoded PCM.
type Layout struct {
	channels []Channel
	weights  []float64
}

// NewLayout validates a channel-label list and computes weights. It rejects
// empty and oversized layouts, unknown labels and duplicate labels. Layouts
// that are not in StandardLayouts are only accepted once the caller has
// supplied the labels explicitly with --layout.
func NewLayout(labels []string) (Layout, error) {
	if len(labels) == 0 {
		return Layout{}, fmt.Errorf("empty channel layout")
	}
	if len(labels) > 8 {
		return Layout{}, fmt.Errorf("unsupported layout with %d channels (max 8)", len(labels))
	}
	seen := make(map[string]bool, len(labels))
	l := Layout{channels: make([]Channel, 0, len(labels)), weights: make([]float64, 0, len(labels))}
	for i, raw := range labels {
		c := Channel(raw)
		if _, ok := knownLabels[c]; !ok {
			return Layout{}, fmt.Errorf("unknown channel label %q at index %d", raw, i)
		}
		if seen[raw] {
			return Layout{}, fmt.Errorf("duplicate channel label %q", raw)
		}
		seen[raw] = true
		l.channels = append(l.channels, c)
		l.weights = append(l.weights, weight(c))
	}
	return l, nil
}

var knownLabels = map[Channel]struct{}{
	ChannelFrontLeft: {}, ChannelFrontRight: {}, ChannelFrontCenter: {},
	ChannelLFE: {}, ChannelBackLeft: {}, ChannelBackRight: {},
	ChannelSideLeft: {}, ChannelSideRight: {},
}

// Len returns the channel count.
func (l Layout) Len() int { return len(l.channels) }

// Channels returns a copy of the label list.
func (l Layout) Channels() []Channel {
	out := make([]Channel, len(l.channels))
	copy(out, l.channels)
	return out
}

// Weight returns the BS.1770 weight for channel i (0 for LFE).
func (l Layout) Weight(i int) float64 { return l.weights[i] }

// SumSquaresWeighted accumulates a weighted frame sum-of-squares. It is the
// hot path shared by the loudness meter, so it avoids allocations.
func (l Layout) SumSquaresWeighted(frame []float64) float64 {
	var sum float64
	for i, w := range l.weights {
		if w == 0 {
			continue
		}
		s := frame[i]
		sum += w * s * s
	}
	return sum
}

// String renders the layout as a dash separated list, e.g. "FL-FR-FC-LFE-BL-BR".
func (l Layout) String() string {
	s := ""
	for i, c := range l.channels {
		if i > 0 {
			s += "-"
		}
		s += string(c)
	}
	return s
}

// StandardLayouts maps the ffmpeg channel-layout names we accept without an
// explicit --layout override. Only layouts whose every channel has a defined
// BS.1770 weight, and that fit the 8-channel ceiling, appear here. Others
// (6.1 with a back centre, 5.1.2 with height channels, binaural, ...) must be
// declared with --layout.
var StandardLayouts = map[string][]string{
	"mono":       {"FC"},
	"stereo":     {"FL", "FR"},
	"2.1":        {"FL", "FR", "LFE"},
	"3.0":        {"FL", "FR", "FC"},
	"3.1":        {"FL", "FR", "FC", "LFE"},
	"quad":       {"FL", "FR", "BL", "BR"},
	"quad(side)": {"FL", "FR", "SL", "SR"},
	"5.0":        {"FL", "FR", "FC", "BL", "BR"},
	"5.0(side)":  {"FL", "FR", "FC", "SL", "SR"},
	"5.1":        {"FL", "FR", "FC", "LFE", "BL", "BR"},
	"5.1(side)":  {"FL", "FR", "FC", "LFE", "SL", "SR"},
	"7.0":        {"FL", "FR", "FC", "BL", "BR", "SL", "SR"},
	"7.1":        {"FL", "FR", "FC", "LFE", "BL", "BR", "SL", "SR"},
}
