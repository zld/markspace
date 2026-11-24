package demux

import (
	"crypto/sha256"
	"encoding/hex"

	"mediapack/internal/clock"
	"mediapack/internal/fault"
)

// CodecParams contains the container codec configuration needed to preserve a
// track without decoding or re-encoding its compressed samples.
type CodecParams struct {
	Codec      string `json:"codec"`
	FourCC     string `json:"four_cc,omitempty"`
	Width      uint16 `json:"width,omitempty"`
	Height     uint16 `json:"height,omitempty"`
	Channels   uint16 `json:"channels,omitempty"`
	SampleRate uint32 `json:"sample_rate,omitempty"`
	ConfigHex  string `json:"config_hex,omitempty"`
}

// Sample is one preserved compressed sample. Offset is its position in the
// original input and Hash is the SHA-256 of exactly Payload.
type Sample struct {
	Index    int    `json:"index"`
	TrackID  uint32 `json:"track_id"`
	DTS      int64  `json:"dts"`
	PTS      int64  `json:"pts"`
	Duration int64  `json:"duration"`
	Offset   int64  `json:"offset"`
	Size     int    `json:"size"`
	Flags    uint32 `json:"flags,omitempty"`
	Hash     string `json:"payload_hash"`
	Payload  []byte `json:"-"`
	Gap      bool   `json:"gap,omitempty"`
}

// Track is the demuxed metadata and ordered sample table for one source track.
type Track struct {
	ID       uint32         `json:"id"`
	Kind     string         `json:"kind"`
	Codec    CodecParams    `json:"codec"`
	Timebase clock.Timebase `json:"timebase"`
	Duration int64          `json:"duration"`
	Samples  []*Sample      `json:"samples"`
}

// Document is a parsed local input. Issues are non-fatal isolated regions.
type Document struct {
	Path   string         `json:"path"`
	Format string         `json:"format"`
	Tracks []*Track       `json:"tracks"`
	Issues []*fault.Error `json:"issues,omitempty"`
}

func (d *Document) Track(id uint32) *Track {
	for _, t := range d.Tracks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func HashPayload(p []byte) string {
	sum := sha256.Sum256(p)
	return hex.EncodeToString(sum[:])
}

// ErrorRange is a JSON-friendly isolated bad interval.
type ErrorRange struct {
	Path        string `json:"path"`
	Kind        string `json:"kind"`
	TrackID     uint32 `json:"track_id,omitempty"`
	StartOffset int64  `json:"start_offset"`
	EndOffset   int64  `json:"end_offset,omitempty"`
	Recoverable bool   `json:"recoverable"`
	Message     string `json:"message"`
}

func (d *Document) ErrorRanges() []ErrorRange {
	out := make([]ErrorRange, 0, len(d.Issues))
	for _, e := range d.Issues {
		end := e.Offset
		out = append(out, ErrorRange{
			Path: e.Path, Kind: string(e.Kind), TrackID: e.TrackID,
			StartOffset: e.Offset, EndOffset: end,
			Recoverable: e.Recoverable, Message: e.Error(),
		})
	}
	return out
}
