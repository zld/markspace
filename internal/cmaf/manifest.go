package cmaf

import (
	"encoding/json"
	"os"
)

// EventKind enumerates ordered input manifest records.
type EventKind string

const (
	EventInit     EventKind = "init"
	EventFragment EventKind = "fragment"
	EventRTP      EventKind = "rtp"
	EventAnchor   EventKind = "anchor"
	EventSlow     EventKind = "slow_consumer"
)

// Event is one ordered manifest item. Paths are relative to the manifest
// directory unless absolute.
type Event struct {
	Index    int               `json:"index"`
	Kind     EventKind         `json:"kind"`
	SourceID string            `json:"source_id,omitempty"`
	TrackID  uint32            `json:"track_id,omitempty"`
	Path     string            `json:"path,omitempty"`
	Sequence uint32            `json:"sequence,omitempty"`
	RTP      *RTPSpec          `json:"rtp,omitempty"`
	Anchor   *AnchorSpec       `json:"anchor,omitempty"`
	Slow     *SlowConsumerSpec `json:"slow,omitempty"`
}

// RTPSpec is fixture/input RTP trace metadata. Packets may also be supplied as
// raw hex/base64 in a sidecar in a fuller implementation; the fixture uses
// these deterministic fields.
type RTPSpec struct {
	SSRC      uint32 `json:"ssrc"`
	ClockRate uint32 `json:"clock_rate"`
}

// AnchorSpec correlates an RTP timestamp with a media track boundary.
type AnchorSpec struct {
	AnchorID   string `json:"anchor_id"`
	SourceID   string `json:"source_id"`
	TrackID    uint32 `json:"track_id"`
	RTPTime    uint32 `json:"rtp_time"`
	MediaRaw   int64  `json:"media_raw"`
	Timescale  uint32 `json:"timescale"`
	ExpectedUs int64  `json:"expected_us"`
}

// SlowConsumerSpec asks the verification pipeline to simulate a slow sink.
type SlowConsumerSpec struct {
	DelayMS int `json:"delay_ms"`
	Items   int `json:"items"`
}

// Manifest is the ordered input contract.
type Manifest struct {
	Case     string          `json:"case"`
	InitPath string          `json:"init_path"`
	Events   []Event         `json:"events"`
	RTPTrace []RTPPacketSpec `json:"rtp_trace,omitempty"`
}

// RTPPacketSpec is a serializable pion/rtp-like packet.
type RTPPacketSpec struct {
	Sequence    uint16 `json:"sequence"`
	Timestamp   uint32 `json:"timestamp"`
	SSRC        uint32 `json:"ssrc"`
	Marker      bool   `json:"marker"`
	Payload     []byte `json:"payload,omitempty"`
	IsRetrans   bool   `json:"is_retransmission,omitempty"`
	OriginalSeq uint16 `json:"original_sequence,omitempty"`
}

// LoadManifest reads a JSON manifest.
func LoadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for i := range m.Events {
		m.Events[i].Index = i
	}
	return &m, nil
}
