package model

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

type TrackType string

const (
	TrackVideo    TrackType = "video"
	TrackAudio    TrackType = "audio"
	TrackSubtitle TrackType = "subtitle"
	TrackChapter  TrackType = "chapter"
	TrackMetadata TrackType = "metadata"
)

type Codec string

const (
	CodecSyntheticVideo Codec = "synv"
	CodecSyntheticAudio Codec = "syna"
	CodecWebVTT         Codec = "wvtt"
	CodecChapter        Codec = "chap"
	CodecID3            Codec = "id3"
	CodecEmsg           Codec = "emsg"
	CodecSEI            Codec = "sei"
)

// ChannelLayout is an explicit, comparable audio layout. Layouts are never
// silently downmixed.
type ChannelLayout string

const (
	LayoutMono   ChannelLayout = "mono"
	LayoutStereo ChannelLayout = "stereo"
	Layout51     ChannelLayout = "5.1"
	Layout71     ChannelLayout = "7.1"
)

func LayoutChannels(l ChannelLayout) int {
	switch l {
	case LayoutMono:
		return 1
	case LayoutStereo:
		return 2
	case Layout51:
		return 6
	case Layout71:
		return 8
	default:
		return 0
	}
}

// ColorMeta retains the source values. A zero/empty field means absent.
type ColorMeta struct {
	Primaries         uint16 `json:"primaries,omitempty"`
	Transfer          uint16 `json:"transfer,omitempty"`
	Matrix            uint16 `json:"matrix,omitempty"`
	FullRange         bool   `json:"full_range,omitempty"`
	SourceDescription string `json:"source_description,omitempty"`
}

func (c ColorMeta) ConflictFields(o ColorMeta) []string {
	var fields []string
	if c.Primaries != o.Primaries && (c.Primaries != 0 && o.Primaries != 0) {
		fields = append(fields, "color_primaries")
	}
	if c.Transfer != o.Transfer && (c.Transfer != 0 && o.Transfer != 0) {
		fields = append(fields, "transfer_characteristics")
	}
	if c.Matrix != o.Matrix && (c.Matrix != 0 && o.Matrix != 0) {
		fields = append(fields, "matrix_coefficients")
	}
	if c.FullRange != o.FullRange {
		fields = append(fields, "full_range_flag")
	}
	sort.Strings(fields)
	return fields
}

type Track struct {
	ID            uint32        `json:"id"`
	Type          TrackType     `json:"type"`
	Codec         Codec         `json:"codec"`
	TimeBase      TimeBase      `json:"time_base"`
	DurationTicks int64         `json:"duration_ticks"`
	SampleRate    int           `json:"sample_rate,omitempty"`
	Channels      int           `json:"channels,omitempty"`
	Layout        ChannelLayout `json:"layout,omitempty"`
	Width         int           `json:"width,omitempty"`
	Height        int           `json:"height,omitempty"`
	Color         *ColorMeta    `json:"color,omitempty"`
	Required      bool          `json:"required,omitempty"`
}

type Sample struct {
	TrackID           uint32 `json:"track_id"`
	Index             int    `json:"index"`
	PTS               int64  `json:"pts"`
	DTS               int64  `json:"dts"`
	Duration          int64  `json:"duration"`
	KeyFrame          bool   `json:"key_frame,omitempty"`
	Payload           []byte `json:"payload"`
	CompositionOffset int64  `json:"composition_offset,omitempty"`
}

type MetadataKind string

const (
	MetaSubtitle MetadataKind = "subtitle"
	MetaChapter  MetadataKind = "chapter"
	MetaID3      MetadataKind = "id3"
	MetaEmsg     MetadataKind = "emsg"
	MetaSEI      MetadataKind = "sei"
)

type TimedEvent struct {
	ID           string            `json:"id"`
	Kind         MetadataKind      `json:"kind"`
	TrackID      uint32            `json:"track_id"`
	PTS          Timestamp         `json:"pts"`
	DurationMS   int64             `json:"duration_ms,omitempty"`
	Payload      []byte            `json:"payload"`
	Text         string            `json:"text,omitempty"`
	Fields       map[string]string `json:"fields,omitempty"`
	SourceOffset int64             `json:"source_offset,omitempty"`
}

type Archive struct {
	Brand       string       `json:"brand"`
	Tracks      []Track      `json:"tracks"`
	Samples     []Sample     `json:"samples"`
	Events      []TimedEvent `json:"events"`
	RPTPackets  []RTPPacket  `json:"rtp_packets,omitempty"`
	GeneratedBy string       `json:"generated_by"`
}

// RTPPacket is the deterministic, serialization-friendly projection of an
// RTP packet. Payload is retained for recovery/hash checks.
type RTPPacket struct {
	Sequence     uint16 `json:"sequence"`
	Timestamp    uint32 `json:"timestamp"`
	SSRC         uint32 `json:"ssrc"`
	Marker       bool   `json:"marker"`
	PayloadType  uint8  `json:"payload_type"`
	Payload      []byte `json:"payload"`
	InjectedLoss bool   `json:"injected_loss,omitempty"`
	SourceOffset int64  `json:"source_offset,omitempty"`
}

func (a *Archive) Track(id uint32) *Track {
	for i := range a.Tracks {
		if a.Tracks[i].ID == id {
			return &a.Tracks[i]
		}
	}
	return nil
}

func (a *Archive) SamplesFor(id uint32) []Sample {
	var out []Sample
	for _, s := range a.Samples {
		if s.TrackID == id {
			out = append(out, s)
		}
	}
	return out
}

// SampleHashes returns stable per-track payload hashes.
func (a *Archive) SampleHashes() map[uint32]string {
	out := make(map[uint32]string, len(a.Tracks))
	for _, tr := range a.Tracks {
		h := sha256.New()
		for _, s := range a.Samples {
			if s.TrackID == tr.ID {
				h.Write([]byte{byte(s.Index >> 24), byte(s.Index >> 16), byte(s.Index >> 8), byte(s.Index)})
				h.Write(s.Payload)
			}
		}
		out[tr.ID] = hex.EncodeToString(h.Sum(nil))
	}
	return out
}
