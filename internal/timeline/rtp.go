package timeline

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/pion/rtp"
)

// RTPLogEntry is one parsed RTP packet observation from a client log line.
type RTPLogEntry struct {
	SSRC       uint32
	Timestamp  uint32
	Sequence   uint16
	PayloadLen int
	// RecvNS is the wall-clock receive time already expressed in nanoseconds,
	// taken from the log record rather than inferred from the RTP clock.
	RecvNS int64
}

// RTPStream is the nanosecond timeline reconstructed for one SSRC.
type RTPStream struct {
	SSRC        uint32
	ClockRateHz uint32
	Packets     []RTPLogEntry
	clock       RTPClock
}

// NewRTPStream builds a stream. The first packet anchors the RTP clock to its
// logged receive instant; subsequent packets are positioned by RTP timestamp
// wrapping arithmetic.
func NewRTPStream(ssrc uint32, clockRateHz uint32, packets []RTPLogEntry) (*RTPStream, error) {
	if clockRateHz == 0 {
		return nil, fmt.Errorf("SSRC %d: zero clock rate", ssrc)
	}
	if len(packets) == 0 {
		return nil, fmt.Errorf("SSRC %d: no packets", ssrc)
	}
	s := &RTPStream{SSRC: ssrc, ClockRateHz: clockRateHz, Packets: packets}
	s.clock = RTPClock{
		SSRC:        ssrc,
		ClockRateHz: clockRateHz,
		FirstRTP:    packets[0].Timestamp,
		FirstNS:     packets[0].RecvNS,
	}
	return s, nil
}

// TimeNS converts a packet's RTP timestamp to the shared nanosecond clock.
func (s *RTPStream) TimeNS(ts uint32) int64 {
	return s.clock.RTPToNSExact(ts)
}

// ParseRTPPacket decodes a single RTP datagram (binary, not the log envelope).
func ParseRTPPacket(raw []byte) (*rtp.Packet, error) {
	p := &rtp.Packet{}
	if err := p.Unmarshal(raw); err != nil {
		return nil, fmt.Errorf("rtp unmarshal: %w", err)
	}
	return p, nil
}

// DecodeHexPacket parses an RTP packet supplied as a hexadecimal string, as
// found in JSONL log records.
func DecodeHexPacket(hexPacket string) (*rtp.Packet, error) {
	raw, err := hex.DecodeString(hexPacket)
	if err != nil {
		return nil, fmt.Errorf("rtp hex decode: %w", err)
	}
	return ParseRTPPacket(raw)
}

// EntryFromPacket converts a parsed packet plus a receive instant into a
// log entry.
func EntryFromPacket(p *rtp.Packet, recvNS int64) RTPLogEntry {
	return RTPLogEntry{
		SSRC:       p.SSRC,
		Timestamp:  p.Timestamp,
		Sequence:   p.SequenceNumber,
		PayloadLen: len(p.Payload),
		RecvNS:     recvNS,
	}
}

// ValidateOrdering reports a descriptive error when RTP timestamps move
// backwards relative to the previous packet on the same stream.
func ValidateOrdering(packets []RTPLogEntry) error {
	for i := 1; i < len(packets); i++ {
		delta := int32(packets[i].Timestamp - packets[i-1].Timestamp)
		if delta < 0 {
			return fmt.Errorf("%w: seq=%d rtp delta=%d ticks",
				ErrTimeRegression, packets[i].Sequence, delta)
		}
	}
	return nil
}

// ErrTimeRegression signals a backwards timestamp on a timeline.
var ErrTimeRegression = errors.New("timeline time regression")
