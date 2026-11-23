package timeline

import "fmt"

// Kind identifies a track's media type.
type Kind uint8

const (
	KindUnknown Kind = iota
	KindVideo
	KindAudio
)

func (k Kind) String() string {
	switch k {
	case KindVideo:
		return "video"
	case KindAudio:
		return "audio"
	default:
		return "unknown"
	}
}

// ChannelLayout is an explicit speaker/channel arrangement. Only layouts the
// normalizer knows how to down/up-map are accepted.
type ChannelLayout uint8

const (
	LayoutUnknown     ChannelLayout = 0
	LayoutMono        ChannelLayout = 1
	LayoutStereo      ChannelLayout = 2
	LayoutFiveOne     ChannelLayout = 6 // 5.1: L R C LFE Ls Rs
	LayoutUnsupported ChannelLayout = 255
)

// Channels returns the number of PCM channels in the layout.
func (l ChannelLayout) Channels() int {
	switch l {
	case LayoutMono:
		return 1
	case LayoutStereo:
		return 2
	case LayoutFiveOne:
		return 6
	default:
		return 0
	}
}

func (l ChannelLayout) String() string {
	switch l {
	case LayoutMono:
		return "mono"
	case LayoutStereo:
		return "stereo"
	case LayoutFiveOne:
		return "5.1"
	default:
		return "unsupported"
	}
}

// ColorSource records where color metadata came from; provenance is mandatory
// because conflict arbitration is performed by source priority.
type ColorSource string

const (
	SourceNone      ColorSource = ""          // missing, still pending
	SourceContainer ColorSource = "container" // colr box inside the MP4/MOV
	SourceBitstream ColorSource = "bitstream" // signaled inside coded samples
	SourceCLI       ColorSource = "cli"       // forced via command parameter
)

// ColorDesc is a resolved NCLX-style color description plus its provenance.
type ColorDesc struct {
	Primaries uint16
	Transfer  uint16
	Matrix    uint16
	FullRange bool
	Source    ColorSource
}

// Equal reports whether two descriptions describe the same color pipeline.
func (d ColorDesc) Equal(o ColorDesc) bool {
	return d.Primaries == o.Primaries && d.Transfer == o.Transfer &&
		d.Matrix == o.Matrix && d.FullRange == o.FullRange
}

// TrackSample is one entry of a track's decoded sample table on the common
// clock. Keyframe applies to video only.
type TrackSample struct {
	DTS      uint64 // decode timestamp, common-clock ticks
	CTS      uint64 // composition timestamp, common-clock ticks
	Dur      uint64 // duration on the common clock
	Size     uint32 // coded size in bytes
	Keyframe bool
}

// Track is one node of the explicit track graph. Native time-base values are
// retained (Timescale) alongside every timestamp mapped to the common clock.
type Track struct {
	ID         uint32
	Kind       Kind
	Timescale  uint32
	Layout     ChannelLayout // audio only
	SampleRate uint32        // audio only
	Color      *ColorDesc    // video only; nil when metadata is absent
	// ColorCandidates holds every description keyed by its source. The
	// normalizer arbitrates conflicts by source priority; an empty map with a
	// nil Color marks metadata as missing/pending.
	ColorCandidates map[ColorSource]ColorDesc
	Samples         []TrackSample
}

// End returns the common-clock end (last sample end) of the track.
func (t *Track) End() uint64 {
	if len(t.Samples) == 0 {
		return 0
	}
	last := t.Samples[len(t.Samples)-1]
	if last.CTS >= last.DTS {
		return last.CTS + last.Dur
	}
	return last.DTS + last.Dur
}

// Graph is the explicit set of tracks parsed from one container.
type Graph struct {
	Tracks []*Track
}

// ByKind returns the first track of the given kind, or nil.
func (g *Graph) ByKind(k Kind) *Track {
	for _, t := range g.Tracks {
		if t.Kind == k {
			return t
		}
	}
	return nil
}

// Locate renders a track/stage reference for error messages.
func (t *Track) Locate(stage string) string {
	return fmt.Sprintf("stage=%s track_id=%d kind=%s", stage, t.ID, t.Kind)
}
