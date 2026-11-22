package demux

import (
	"fmt"

	"mediasync/internal/clock"
)

// Matroska track type codes (TrackType element).
const (
	TypeVideo    uint64 = 1
	TypeAudio    uint64 = 2
	TypeSubtitle uint64 = 17
)

// Kind labels a mapping failure so the command layer can pick exit code 2
// (input/mapping error) without inspecting messages.
type Kind int

const (
	KindOK Kind = iota
	KindMissingTrack
	KindDuplicateTarget
	KindIncompatibleCodec
)

// MapError describes a rejected explicit track mapping before any output is
// written.
type MapError struct {
	Kind   Kind
	Source uint32
	Target string
	Msg    string
}

func (e *MapError) Error() string {
	return fmt.Sprintf("mapping: %s (source=%d target=%q)", e.Msg, e.Source, e.Target)
}

// Track is one source track as described by the Matroska Tracks header.
type Track struct {
	Number          uint32 // TrackNumber used inside blocks
	UID             uint64
	Type            uint64
	CodecID         string
	CodecPrivate    []byte
	TimecodeScaleNS int64 // resolved TimecodeScale, shared default when unset
	DefaultDuration int64 // nanoseconds when present
	VideoWidth      uint64
	VideoHeight     uint64
	AudioFreq       float64
	AudioChannels   uint64
}

// Role is the requested output role for a mapped track.
type Role string

const (
	RoleVideo    Role = "video"
	RoleAudio    Role = "audio"
	RoleSubtitle Role = "subtitle"
)

// supportedCodec maps a Matroska CodecID to the role it may serve. Only the
// common WebM codecs are promised; anything else fails validation rather
// than being remuxed into a container that cannot represent it.
var supportedCodec = map[string]Role{
	"V_VP8":              RoleVideo,
	"V_VP9":              RoleVideo,
	"A_OPUS":             RoleAudio,
	"A_VORBIS":           RoleAudio,
	"D_WEBVTT/SUBTITLES": RoleSubtitle,
	"S_TEXT/WEBVTT":      RoleSubtitle,
}

// Binding binds one Matroska source track number to an output role.
type Binding struct {
	Source uint32
	Role   Role
}

// Mapping is the resolved, validated binding table handed to the writer.
type Mapping struct {
	bindings map[uint32]Binding
	tracks   map[uint32]*Track
}

// Resolve validates an explicit mapping table against the tracks declared in
// the header. Missing sources, duplicate roles and codecs the output cannot
// carry all fail here — before a single block is written.
func Resolve(tracks []*Track, bindings []Binding) (*Mapping, error) {
	byNumber := make(map[uint32]*Track, len(tracks))
	for _, tr := range tracks {
		byNumber[tr.Number] = tr
	}
	m := &Mapping{
		bindings: make(map[uint32]Binding, len(bindings)),
		tracks:   make(map[uint32]*Track, len(bindings)),
	}
	roles := make(map[Role]uint32)
	for _, b := range bindings {
		tr, ok := byNumber[b.Source]
		if !ok {
			return nil, &MapError{Kind: KindMissingTrack, Source: b.Source, Target: string(b.Role),
				Msg: fmt.Sprintf("source track %d not present in container", b.Source)}
		}
		// Structural errors (duplicate output role) are reported before codec
		// compatibility so the mapping table is rejected as a whole first.
		if prev, dup := roles[b.Role]; dup {
			return nil, &MapError{Kind: KindDuplicateTarget, Source: b.Source, Target: string(b.Role),
				Msg: fmt.Sprintf("role %q already bound to source track %d", b.Role, prev)}
		}
		role, ok := supportedCodec[tr.CodecID]
		if !ok {
			return nil, &MapError{Kind: KindIncompatibleCodec, Source: b.Source, Target: string(b.Role),
				Msg: fmt.Sprintf("codec %q is not supported for lossless remux", tr.CodecID)}
		}
		if role != b.Role {
			return nil, &MapError{Kind: KindIncompatibleCodec, Source: b.Source, Target: string(b.Role),
				Msg: fmt.Sprintf("codec %q serves role %q, not %q", tr.CodecID, role, b.Role)}
		}
		roles[b.Role] = b.Source
		m.bindings[b.Source] = b
		m.tracks[b.Source] = tr
	}
	return m, nil
}

// Sources returns the bound source track numbers in ascending order, giving
// downstream code a deterministic iteration order.
func (m *Mapping) Sources() []uint32 {
	out := make([]uint32, 0, len(m.bindings))
	for n := range m.bindings {
		out = append(out, n)
	}
	sortU32(out)
	return out
}

// Track returns the header for a bound source.
func (m *Mapping) Track(source uint32) (*Track, bool) {
	tr, ok := m.tracks[source]
	return tr, ok
}

// ClockConfigs derives clock.TrackClock entries for every bound track using
// the container TimecodeScale and the caller's wrap/drift parameters.
func (m *Mapping) ClockConfigs(wrapAt int64, driftPPM map[uint32]float64) []clock.TrackClock {
	out := make([]clock.TrackClock, 0, len(m.bindings))
	for _, n := range m.Sources() {
		tr := m.tracks[n]
		// Matroska block timecodes tick in 1ms units scaled by TimecodeScale.
		tps := int64(1_000_000_000 / tr.TimecodeScaleNS)
		if tps <= 0 {
			tps = 1000
		}
		out = append(out, clock.TrackClock{
			Track:          n,
			TicksPerSecond: tps,
			WrapAt:         wrapAt,
			DriftPPM:       driftPPM[n],
		})
	}
	return out
}

func sortU32(a []uint32) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}
