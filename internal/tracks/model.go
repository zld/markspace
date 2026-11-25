package tracks

import (
	"math"
	"sort"
	"strings"

	"example.com/assetcheck/internal/timeline"
)

// TrackType is the Matroska TrackType enumeration.
type TrackType uint64

const (
	TypeVideo    TrackType = 1
	TypeAudio    TrackType = 2
	TypeComplex  TrackType = 3
	TypeLogo     TrackType = 4
	TypeSubtitle TrackType = 17
	TypeButtons  TrackType = 18
	TypeControl  TrackType = 32
	TypeMetadata TrackType = 33
)

// Name returns a stable label used in CLI output and hashes.
func (t TrackType) Name() string {
	switch t {
	case TypeVideo:
		return "video"
	case TypeAudio:
		return "audio"
	case TypeComplex:
		return "complex"
	case TypeLogo:
		return "logo"
	case TypeSubtitle:
		return "subtitle"
	case TypeButtons:
		return "buttons"
	case TypeControl:
		return "control"
	case TypeMetadata:
		return "metadata"
	default:
		return "unknown"
	}
}

func (t TrackType) Valid() bool {
	switch t {
	case TypeVideo, TypeAudio, TypeComplex, TypeLogo, TypeSubtitle,
		TypeButtons, TypeControl, TypeMetadata:
		return true
	}
	return false
}

// ChannelLayout declares the expected physical channel arrangement for an
// audio track. The layout's channel count must agree with Matroska Channels.
type ChannelLayout string

const (
	LayoutMono   ChannelLayout = "mono"   // 1
	LayoutStereo ChannelLayout = "stereo" // 2
	Layout2_1    ChannelLayout = "2.1"    // 3
	LayoutQuad   ChannelLayout = "quad"   // 4
	Layout5_0    ChannelLayout = "5.0"    // 5
	Layout5_1    ChannelLayout = "5.1"    // 6
	Layout6_1    ChannelLayout = "6.1"    // 7
	Layout7_1    ChannelLayout = "7.1"    // 8
)

// ChannelsFor returns the channel count implied by a standard layout.
func (l ChannelLayout) ChannelsFor() (uint64, bool) {
	switch l {
	case LayoutMono:
		return 1, true
	case LayoutStereo:
		return 2, true
	case Layout2_1:
		return 3, true
	case LayoutQuad:
		return 4, true
	case Layout5_0:
		return 5, true
	case Layout5_1:
		return 6, true
	case Layout6_1:
		return 7, true
	case Layout7_1:
		return 8, true
	}
	return 0, false
}

// Audio describes the audio parameters that must agree across the pipeline.
type Audio struct {
	Channels           uint64
	SamplingFrequency  float64 // Hz
	OutputSamplingFreq float64 // Hz, 0 when absent
	BitDepth           uint64
	Layout             ChannelLayout
}

// EffectiveRate returns the output rate when present, else the input rate.
func (a Audio) EffectiveRate() float64 {
	if a.OutputSamplingFreq > 0 {
		return a.OutputSamplingFreq
	}
	return a.SamplingFrequency
}

// Colour captures the CICP/HDR metadata that must be complete and consistent.
type Colour struct {
	Present              bool
	MatrixCoefficients   uint64
	TransferCharacterist uint64
	Primaries            uint64
	Range                uint64
	BitsPerChannel       uint64
	ChromaSubsampleHorz  uint64
	ChromaSubsampleVert  uint64
	MaxCLL               uint64
	MaxFALL              uint64
	HasMaxCLL            bool
	HasMaxFALL           bool
	MasteringPresent     bool
}

// IsComplete reports whether the three mandatory CICP fields are present. A
// Colour block without them cannot be mapped reliably during migration.
func (c Colour) IsComplete() bool {
	if !c.Present {
		return false
	}
	// CICP value 2 means "unspecified", which is not usable metadata.
	if c.MatrixCoefficients == 0 || c.MatrixCoefficients == 2 {
		return false
	}
	if c.TransferCharacterist == 0 || c.TransferCharacterist == 2 {
		return false
	}
	if c.Primaries == 0 || c.Primaries == 2 {
		return false
	}
	return true
}

// Video describes the picture and colour metadata of a video track.
type Video struct {
	PixelWidth    uint64
	PixelHeight   uint64
	DisplayWidth  uint64
	DisplayHeight uint64
	Colour        Colour
}

// Track is the normalized track description every subsystem consumes.
type Track struct {
	Number          uint64
	UID             uint64
	Type            TrackType
	CodecID         string
	Language        string
	Name            string
	Offset          int64 // absolute offset of the TrackEntry element
	DefaultDuration uint64
	Audio           *Audio
	Video           *Video
}

// Label renders the track for diagnostics: number, type and name.
func (t Track) Label() string {
	var b strings.Builder
	b.WriteString("track ")
	b.WriteString(itoa(t.Number))
	b.WriteString(" (")
	b.WriteString(t.Type.Name())
	b.WriteString(")")
	if t.Name != "" {
		b.WriteString(" ")
		b.WriteString(t.Name)
	}
	return b.String()
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// SortTracks orders tracks by number so output and hashes are deterministic.
func SortTracks(ts []Track) {
	sort.Slice(ts, func(i, j int) bool { return ts[i].Number < ts[j].Number })
}

// Profile is the manifest-declared envelope a media file must stay inside.
type Profile struct {
	AudioRates      map[uint64]RateRange  // by track number; empty means any
	VideoColour     map[uint64]ColourRule // by track number
	Layouts         map[uint64]ChannelLayout
	MaxChannels     uint64
	RequireColour   bool
	AllowedRateSpan float64 // max output/input rate mismatch fraction
}

// RateRange is an inclusive sampling-rate window in Hz.
type RateRange struct {
	Min float64 `json:"min_hz"`
	Max float64 `json:"max_hz"`
}

// Contains reports whether hz is inside the window (inclusive).
func (r RateRange) Contains(hz float64) bool { return hz >= r.Min && hz <= r.Max }

// ColourRule constrains the CICP triple for a video track. The JSON tags make
// it the canonical manifest encoding as well.
type ColourRule struct {
	MatrixCoefficients uint64 `json:"matrix_coefficients"`
	Transfer           uint64 `json:"transfer_characteristics"`
	Primaries          uint64 `json:"primaries"`
	Range              uint64 `json:"range,omitempty"`
}

func nearlyEqual(a, b float64) bool {
	return math.Abs(a-b) <= 1e-6*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}

// Issue is one validation failure, attributed to specific tracks and the
// earliest locatable input offset.
type Issue struct {
	Category timeline.Category
	Offset   int64
	Tracks   []uint64
	Field    string
	Reason   string
}

// ToError converts the first issue into the shared error type.
func (i Issue) ToError(stage timeline.Stage) *timeline.Error {
	return &timeline.Error{
		Stage:    stage,
		Category: i.Category,
		Offset:   i.Offset,
		Tracks:   i.Tracks,
		Field:    i.Field,
		Reason:   i.Reason,
	}
}
