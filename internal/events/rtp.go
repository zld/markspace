// Package events parses JSONL event feeds whose timestamps arrive as RTP
// packets and maps them onto the same rational media clock as the
// container. Bad packets are counted and isolated: later valid events in
// the same feed still get processed.
package events

import (
	"fmt"

	"github.com/pion/rtp"

	"replay/internal/clock"
)

// Anchor is the explicit --rtp-anchor mapping: RTP timestamp RtpTick at
// media time MediaTick (both on the configured RTP clock frequency).
type Anchor struct {
	RtpTick   uint32
	MediaTick int64
}

// Stream keeps the state needed to unwrap 32-bit RTP timestamps into a
// strictly monotonic 64-bit sequence for one feed.
type Stream struct {
	ClockRate uint32
	Anchor    Anchor

	init    bool
	lastExt int64
	lastRaw uint32
	Wraps   int
	Bad     int
	Total   int
}

// NewStream validates clock configuration at construction.
func NewStream(clockRate uint32, anchor Anchor) (*Stream, error) {
	if clockRate == 0 {
		return nil, clock.NewError(clock.KindConfig, "rtp", "rtp-clock must be > 0")
	}
	return &Stream{ClockRate: clockRate, Anchor: anchor}, nil
}

// unwrap extends a raw 32-bit timestamp to 64 bits. The signed modular
// difference is the shortest signed step on the 32-bit circle, so an
// upward rollover (0xFFFFFFxx -> 0x000000xx) expands as a positive step
// while a genuine backwards step stays negative. The first packet is
// pinned to the explicit anchor rather than to zero.
func (s *Stream) unwrap(raw uint32) int64 {
	if !s.init {
		s.init = true
		s.lastRaw = raw
		s.lastExt = s.Anchor.MediaTick + int64(int32(raw-s.Anchor.RtpTick))
		return s.lastExt
	}
	step := int64(int32(raw - s.lastRaw))
	if step > 0 && raw < s.lastRaw {
		// Positive shortest path that crossed the 2^32 boundary upwards.
		s.Wraps++
	}
	s.lastRaw = raw
	s.lastExt += step
	return s.lastExt
}

// Ingest parses one raw RTP packet, unwraps its timestamp and maps it to
// the media clock. Parse failures increment Bad and return a structured
// error; the stream stays usable for subsequent packets.
func (s *Stream) Ingest(packet []byte) (clock.MediaTime, error) {
	s.Total++
	var pkt rtp.Packet
	if err := pkt.Unmarshal(packet); err != nil {
		s.Bad++
		return clock.MediaTime{}, clock.NewError(clock.KindRTP, "rtp",
			fmt.Sprintf("malformed packet: %v", err))
	}
	ext := s.unwrap(pkt.Timestamp)
	mt, err := clock.NewMediaTime(ext, s.ClockRate)
	if err != nil {
		s.Bad++
		return clock.MediaTime{}, err
	}
	return mt, nil
}

// Stats summarizes feed health for the terminal report and manifest.
type Stats struct {
	Total int `json:"total"`
	Bad   int `json:"bad"`
	Wraps int `json:"wraps"`
}

// Report returns the counters accumulated so far.
func (s *Stream) Report() Stats {
	return Stats{Total: s.Total, Bad: s.Bad, Wraps: s.Wraps}
}
