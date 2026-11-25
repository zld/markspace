package media

import (
	auditerrors "media-audit/internal/errors"
	"media-audit/internal/timebase"
)

type Format string

const (
	FormatMXF   Format = "MXF"
	FormatMP4   Format = "MP4"
	FormatMixed Format = "MIXED"
)

// Crop stores pixel margins removed from the encoded image.
type Crop struct {
	Top    int64 `json:"top"`
	Right  int64 `json:"right"`
	Bottom int64 `json:"bottom"`
	Left   int64 `json:"left"`
}

type Color struct {
	Primaries     uint16 `json:"primaries"`
	Transfer      uint16 `json:"transfer"`
	Matrix        uint16 `json:"matrix"`
	FullRange     bool   `json:"full_range"`
	PrimariesName string `json:"primaries_name,omitempty"`
	TransferName  string `json:"transfer_name,omitempty"`
	MatrixName    string `json:"matrix_name,omitempty"`
}

// Metadata is the format-native declaration before conflict resolution.
type Metadata struct {
	RotationDegrees int32  `json:"rotation_degrees"`
	HasRotation     bool   `json:"has_rotation"`
	Crop            *Crop  `json:"crop,omitempty"`
	Color           *Color `json:"color,omitempty"`
	MetadataVersion uint32 `json:"metadata_version"`
}

type PacketInfo struct {
	Sequence        uint16 `json:"sequence"`
	Timestamp       uint32 `json:"timestamp"`
	PacketOffset    int64  `json:"packet_offset"`
	SequenceOffset  int64  `json:"sequence_offset"`
	TimestampOffset int64  `json:"timestamp_offset"`
}

type Sample struct {
	Index       uint32     `json:"index"`
	KeyFrame    bool       `json:"key_frame"`
	PTS         int64      `json:"pts"`
	Duration    uint32     `json:"duration_ticks"`
	DependsOn   int32      `json:"depends_on"`
	RecordStart int64      `json:"record_start"`
	RecordEnd   int64      `json:"record_end"`
	RTP         PacketInfo `json:"rtp"`
}

type Track struct {
	ID        uint32                `json:"id"`
	Timebase  timebase.Timebase     `json:"timebase"`
	Duration  int64                 `json:"duration_ticks"`
	ByteRange auditerrors.ByteRange `json:"byte_range"`
	Metadata  Metadata              `json:"metadata"`
	Samples   []Sample              `json:"samples"`
	TimeRange *timebase.Range       `json:"unified_range,omitempty"`
}

type Integrity struct {
	Algorithm     string                `json:"algorithm"`
	Range         auditerrors.ByteRange `json:"protected_range"`
	Digest        string                `json:"digest_sha256"`
	ElementOffset int64                 `json:"element_offset"`
}

type Element struct {
	Type       string                `json:"type"`
	Offset     int64                 `json:"offset"`
	HeaderSize int                   `json:"header_size"`
	Range      auditerrors.ByteRange `json:"range"`
}

// Document is the small common media model understood by the audit pipeline.
type Document struct {
	Format          Format               `json:"format"`
	Path            string               `json:"path"`
	Size            int64                `json:"size"`
	Tracks          []Track              `json:"tracks"`
	Integrity       *Integrity           `json:"integrity,omitempty"`
	Elements        []Element            `json:"elements,omitempty"`
	StructureSHA256 string               `json:"structure_sha256,omitempty"`
	Errors          []*auditerrors.Error `json:"errors,omitempty"`
}
