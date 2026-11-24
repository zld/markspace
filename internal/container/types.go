package container

import (
	"github.com/abema/go-mp4"

	"github.com/zld/mediarc/internal/model"
)

// RawBox is a parsed box retained for field-level roundtrip verification.
type RawBox struct {
	Type       string   `json:"type"`
	Offset     int64    `json:"byte_offset"`
	Size       int64    `json:"size"`
	HeaderSize int      `json:"header_size"`
	Depth      int      `json:"depth"`
	TrackIndex int      `json:"track_index"`
	Payload    mp4.IBox `json:"-"`
}

// Track is one parsed MP4/MOV track and its timestamped samples.
type Track struct {
	Metadata model.TrackMetadata `json:"metadata"`

	Tkhd *mp4.Tkhd `json:"-"`
	Mdhd *mp4.Mdhd `json:"-"`
	Hdlr *mp4.Hdlr `json:"-"`
	Stts *mp4.Stts `json:"-"`
	Stss *mp4.Stss `json:"-"`

	Events []model.RawEvent `json:"events"`
}

// File is the validated result of reading an MP4/MOV container.
type File struct {
	Path   string    `json:"path"`
	Size   int64     `json:"size"`
	SHA256 string    `json:"input_sha256"`
	Boxes  []*RawBox `json:"boxes"`
	Tracks []*Track  `json:"tracks"`
}
