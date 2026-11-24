package model

// Event kinds in the capture log. A repair event carries redundancy
// information for a media track rather than media content of its own.
const (
	EventMedia  = "media"
	EventRepair = "repair"
)

// PacketEvent is one line of a capture log. It is the input contract between
// fixtures, the engine and any external capture adapter. Arrival is on the
// recorder's local clock in nanoseconds; media timestamps are raw RTP ticks
// with a configurable wrap width.
type PacketEvent struct {
	TrackID     uint32    `json:"track"`
	Type        string    `json:"type,omitempty"` // "" or "media" for packets
	Kind        TrackKind `json:"kind"`
	SSRC        uint32    `json:"ssrc,omitempty"`
	PayloadType uint8     `json:"pt,omitempty"`
	Marker      bool      `json:"marker,omitempty"`
	Seq         uint32    `json:"seq,omitempty"`
	Timestamp   uint64    `json:"ts,omitempty"`
	Arrival     int64     `json:"arrival_ns"`
	Keyframe    bool      `json:"keyframe,omitempty"`
	Size        int       `json:"size,omitempty"`
	// Carries lists raw sequence numbers on the same track whose content this
	// event repairs. Only meaningful for repair events.
	Carries []uint32 `json:"carries,omitempty"`
}

// IsRepair reports whether the event is an out-of-band repair record.
func (e PacketEvent) IsRepair() bool { return e.Type == EventRepair }

// IsMedia reports whether the event carries media content.
func (e PacketEvent) IsMedia() bool { return e.Type == "" || e.Type == EventMedia }
