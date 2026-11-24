// Package track maps Matroska track numbers onto stable output track numbers
// and centralizes the codec support table. The mapping is deterministic: the
// first track encountered in input order becomes output track 1, and so on,
// so repeated runs over the same input always publish the same mapping.
package track

import (
	"fmt"
	"sort"

	"mediapack/internal/fault"
)

// Kind classifies a track by media type.
type Kind uint8

const (
	KindUnknown  Kind = 0
	KindVideo    Kind = 1
	KindAudio    Kind = 2
	KindSubtitle Kind = 3
)

// String renders the kind for summaries.
func (k Kind) String() string {
	switch k {
	case KindVideo:
		return "video"
	case KindAudio:
		return "audio"
	case KindSubtitle:
		return "subtitle"
	default:
		return "unknown"
	}
}

// Codec describes one supported codec. CodecID is the Matroska CodecID string.
type Codec struct {
	CodecID string
	Name    string
	Kind    Kind
}

// SupportedCodecs is the fixed whitelist. Anything outside it fails loudly
// with an unsupported-codec fault rather than being silently dropped.
var SupportedCodecs = map[string]Codec{
	"V_VP8":         {CodecID: "V_VP8", Name: "VP8", Kind: KindVideo},
	"V_VP9":         {CodecID: "V_VP9", Name: "VP9", Kind: KindVideo},
	"A_OPUS":        {CodecID: "A_OPUS", Name: "Opus", Kind: KindAudio},
	"A_VORBIS":      {CodecID: "A_VORBIS", Name: "Vorbis", Kind: KindAudio},
	"S_TEXT/WEBVTT": {CodecID: "S_TEXT/WEBVTT", Name: "WebVTT", Kind: KindSubtitle},
}

// Track is one input track plus its assigned output position.
type Track struct {
	InputNumber uint32
	OutputIndex uint32 // 1-based, stable across runs
	Codec       Codec
	// TickRate is the track's ticks-per-second used for timebase building.
	TickRate uint64
}

// Mapping is the published, serializable track mapping.
type Mapping struct {
	Tracks []*Track
}

// Registry accumulates tracks in input order and resolves codec support.
type Registry struct {
	byInput map[uint32]*Track
	order   []uint32
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{byInput: make(map[uint32]*Track)}
}

// Register validates the Matroska CodecID and assigns the next stable output
// index. Re-registering the same input number returns the existing entry.
func (r *Registry) Register(inputNumber uint32, codecID string, tickRate uint64) (*Track, error) {
	if existing, ok := r.byInput[inputNumber]; ok {
		return existing, nil
	}
	codec, ok := SupportedCodecs[codecID]
	if !ok {
		return nil, fault.NewTrack(fault.KindUnsupportedCodec, int(inputNumber), -1,
			"codec %q is not in the supported set (VP8/VP9, Opus/Vorbis, WebVTT)",
			codecID)
	}
	if tickRate == 0 {
		return nil, fault.NewTrack(fault.KindTimestampRegression, int(inputNumber), -1,
			"track %d declares no usable tick rate", inputNumber)
	}
	t := &Track{
		InputNumber: inputNumber,
		OutputIndex: uint32(len(r.order) + 1),
		Codec:       codec,
		TickRate:    tickRate,
	}
	r.byInput[inputNumber] = t
	r.order = append(r.order, inputNumber)
	return t, nil
}

// Lookup returns the track registered under an input number.
func (r *Registry) Lookup(inputNumber uint32) (*Track, bool) {
	t, ok := r.byInput[inputNumber]
	return t, ok
}

// Mapping publishes the current registry as an ordered, deterministic mapping.
func (r *Registry) Mapping() *Mapping {
	m := &Mapping{Tracks: make([]*Track, 0, len(r.order))}
	for _, n := range r.order {
		m.Tracks = append(m.Tracks, r.byInput[n])
	}
	return m
}

// ByKind returns tracks of one media kind, preserving output order.
func (m *Mapping) ByKind(k Kind) []*Track {
	var out []*Track
	for _, t := range m.Tracks {
		if t.Codec.Kind == k {
			out = append(out, t)
		}
	}
	return out
}

// Equal reports whether two mappings assign identical output indices and
// codecs, regardless of slice capacity differences.
func (m *Mapping) Equal(other *Mapping) bool {
	if other == nil || len(m.Tracks) != len(other.Tracks) {
		return false
	}
	for i, t := range m.Tracks {
		o := other.Tracks[i]
		if t.InputNumber != o.InputNumber || t.OutputIndex != o.OutputIndex ||
			t.Codec.CodecID != o.Codec.CodecID || t.TickRate != o.TickRate {
			return false
		}
	}
	return true
}

// Render produces stable, sorted text lines for the digest/summary output.
func (m *Mapping) Render() []string {
	lines := make([]string, 0, len(m.Tracks))
	for _, t := range m.Tracks {
		lines = append(lines, fmt.Sprintf(
			"track in=%d -> out=%d codec=%s kind=%s rate=%d",
			t.InputNumber, t.OutputIndex, t.Codec.Name, t.Codec.Kind, t.TickRate))
	}
	sort.Strings(lines)
	return lines
}
