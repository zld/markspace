// Package model holds the core frame data structures, checksum helpers and
// structured errors shared across all pipeline stages.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// FrameNumber is the 1-based ordinal of a frame inside its manifest, in
// manifest order. Zero means "not applicable".
type FrameNumber int

// ChannelLayout describes the channel geometry declared by a frame.
type ChannelLayout string

const (
	LayoutMono   ChannelLayout = "mono"
	LayoutStereo ChannelLayout = "stereo"
)

// Valid reports whether l is a supported layout.
func (l ChannelLayout) Valid() bool {
	return l == LayoutMono || l == LayoutStereo
}

// Channels returns the number of channels for the layout; unknown layouts
// return zero.
func (l ChannelLayout) Channels() int {
	switch l {
	case LayoutMono:
		return 1
	case LayoutStereo:
		return 2
	default:
		return 0
	}
}

// Frame is one manifest entry as parsed from the input manifest. Payload is
// populated by the loader and never persisted inside JSON manifests.
type Frame struct {
	Track          uint32        `json:"track"`
	Sequence       uint32        `json:"sequence"`
	CaptureNanos   int64         `json:"capture_nanos"`
	ContainerID    string        `json:"container_id"`
	SampleRate     uint32        `json:"sample_rate"`
	Channels       ChannelLayout `json:"channels"`
	TargetLUFS     float64       `json:"target_lufs"`
	PayloadLength  uint32        `json:"payload_length"`
	SHA256         string        `json:"sha256"`
	Payload        []byte        `json:"-"`
	Number         FrameNumber   `json:"-"`
	ContainerBytes uint64        `json:"-"` // byte offset inside the MP4 mdat box
}

// OutputFrame is the deterministic result of processing one Frame.
type OutputFrame struct {
	Number        FrameNumber   `json:"number"`
	Track         uint32        `json:"track"`
	Sequence      uint32        `json:"sequence"`
	CaptureNanos  int64         `json:"capture_nanos"`
	RTPTimestamp  uint32        `json:"rtp_timestamp"`
	SampleRate    uint32        `json:"sample_rate"`
	Channels      ChannelLayout `json:"channels"`
	TargetLUFS    float64       `json:"target_lufs"`
	MeasuredLUFS  float64       `json:"measured_lufs"`
	PayloadLength uint32        `json:"payload_length"`
	SHA256        string        `json:"sha256"`
	Payload       []byte        `json:"-"`
}

// Checksum returns the lowercase hex SHA-256 of data.
func Checksum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// CanonicalHash returns the SHA-256 of the canonical JSON encoding of v.
// Struct field order is fixed by declaration, so equal values always hash
// identically across runs and machines.
func CanonicalHash(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return Checksum(data), nil
}

// VerifyChecksum checks the actual payload against an expected lowercase hex
// SHA-256 and returns false on any mismatch.
func VerifyChecksum(payload []byte, expected string) bool {
	return Checksum(payload) == expected
}
