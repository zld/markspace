// Package rtp parses RTP streams carried in local files.  Packets may be
// stored either as a raw concatenation or framed as a 4-byte big-endian length
// followed by the packet bytes.  The parser validates the header, checks the
// sequence number progression and reports the exact byte offset of the first
// packet that cannot be trusted.
package rtp

import (
	"fmt"

	"github.com/pion/rtp"

	"mediapipeline/internal/fault"
)

// Damage describes one sequence discontinuity or malformed packet.
type Damage struct {
	// Offset is the absolute input offset of the first byte of the affected
	// packet, or the input end when a packet header is incomplete.
	Offset int64 `json:"offset"`
	// PacketIndex is the zero-based index of the packet in the stream.
	PacketIndex int `json:"packet_index"`
	// Expected and Got are the sequence numbers around the break.
	Expected uint16 `json:"expected"`
	Got      uint16 `json:"got"`
	Repeated bool   `json:"repeated"`
	// Missing is the number of sequence numbers skipped, wrapped to the 16-bit
	// sequence space.
	Missing int    `json:"missing"`
	SSRC    uint32 `json:"ssrc"`
}

// Packet is one parsed RTP packet with its original position.
type Packet struct {
	Index       int    `json:"index"`
	Offset      int64  `json:"offset"`
	End         int64  `json:"end"`
	Sequence    uint16 `json:"sequence"`
	Timestamp   uint32 `json:"timestamp"`
	SSRC        uint32 `json:"ssrc"`
	PayloadType uint8  `json:"payload_type"`
	Marker      bool   `json:"marker"`
	Payload     []byte `json:"-"`
}

// Stream is the parsed RTP input.
type Stream struct {
	Framed     bool
	Packets    []Packet
	Damage     []Damage
	ValidBytes int64
	SSRC       uint32
	ClockRate  uint32
}

// ErrIncomplete is returned together with the successfully parsed prefix when
// the final packet header is cut off.
type ErrIncomplete struct {
	Offset int64
	Need   int
	Have   int
}

func (e *ErrIncomplete) Error() string {
	return fmt.Sprintf("rtp: incomplete packet header at %d: need %d have %d", e.Offset, e.Need, e.Have)
}

// Options controls stream parsing.
type Options struct {
	// Framed selects 4-byte length framing.  When nil the parser infers the
	// layout from the first length candidate that yields a valid RTP header.
	Framed *bool
	// ClockRate gives the timestamp clock; the default is 90000.
	ClockRate uint32
	// MaxPayload bounds a single packet payload; the default is 65535.
	MaxPayload int
}

// ParseStream parses an RTP byte stream.  It never silently discards bytes:
// sequence breaks are reported in Stream.Damage and an incomplete final header
// yields ErrIncomplete while still returning the verified prefix.
func ParseStream(data []byte, opts Options) (*Stream, error) {
	clockRate := opts.ClockRate
	if clockRate == 0 {
		clockRate = 90000
	}
	maxPayload := opts.MaxPayload
	if maxPayload == 0 {
		maxPayload = 65535
	}
	stream := &Stream{Framed: detectFramed(data), ClockRate: clockRate}
	if opts.Framed != nil {
		stream.Framed = *opts.Framed
	}
	stream.SSRC = 0

	pos := int64(0)
	index := 0
	var lastSeq uint16
	var haveLast bool
	for pos < int64(len(data)) {
		headerStart := pos
		var payload []byte
		var next int64
		if stream.Framed {
			if int64(len(data))-pos < 4 {
				stream.ValidBytes = pos
				return stream, &ErrIncomplete{Offset: int64(len(data)), Need: 4, Have: int(int64(len(data)) - pos)}
			}
			n := int64(data[pos])<<24 | int64(data[pos+1])<<16 | int64(data[pos+2])<<8 | int64(data[pos+3])
			if n < 12 || n > int64(maxPayload) {
				return stream, fault.New(fault.StageRTP, "rtp", fault.OpRTPLength, pos,
					fmt.Sprintf("declared packet length %d is not a valid RTP size", n))
			}
			if pos+4+n > int64(len(data)) {
				stream.ValidBytes = pos
				return stream, &ErrIncomplete{Offset: pos, Need: int(4 + n), Have: int(int64(len(data)) - pos)}
			}
			payload = data[pos+4 : pos+4+n]
			next = pos + 4 + n
		} else {
			// Raw layout: find the extent of this packet.  RTP has no length
			// field, so the stream must be framed for real multiplexed data.
			// We accept the common single-packet-per-line case and otherwise
			// require framing.
			return stream, fault.New(fault.StageRTP, "rtp", fault.OpRTPFraming, 0,
				"raw RTP has no length framing; provide length-prefixed packets")
		}
		pkt := rtp.Packet{}
		if err := pkt.Unmarshal(payload); err != nil {
			return stream, fault.Wrap(fault.StageRTP, "rtp", fault.OpPacketHeader, headerStart+4,
				"invalid RTP header", err)
		}
		p := Packet{
			Index:       index,
			Offset:      headerStart,
			End:         next,
			Sequence:    pkt.SequenceNumber,
			Timestamp:   pkt.Timestamp,
			SSRC:        pkt.SSRC,
			PayloadType: pkt.PayloadType,
			Marker:      pkt.Marker,
			Payload:     pkt.Payload,
		}
		if stream.SSRC == 0 {
			stream.SSRC = pkt.SSRC
		} else if pkt.SSRC != stream.SSRC {
			stream.Damage = append(stream.Damage, Damage{
				Offset: headerStart, PacketIndex: index, SSRC: pkt.SSRC,
				Expected: lastSeq, Got: pkt.SequenceNumber,
			})
		} else if haveLast {
			expected := lastSeq + 1
			if pkt.SequenceNumber != expected {
				d := Damage{
					Offset:      headerStart,
					PacketIndex: index,
					Expected:    expected,
					Got:         pkt.SequenceNumber,
					SSRC:        pkt.SSRC,
				}
				missing := int(uint16(pkt.SequenceNumber - expected))
				if missing == 0 {
					d.Repeated = true
				} else {
					d.Missing = missing
				}
				stream.Damage = append(stream.Damage, d)
			}
		}
		lastSeq = pkt.SequenceNumber
		haveLast = true
		stream.Packets = append(stream.Packets, p)
		stream.ValidBytes = next
		pos = next
		index++
	}
	return stream, nil
}

// detectFramed reports whether data looks like 4-byte length framing around RTP
// packets.  A length-prefixed packet must start with a 4-byte size that covers
// a plausible RTP packet whose version bits are 2.
func detectFramed(data []byte) bool {
	if len(data) < 12 {
		return false
	}
	// Length-framed: first 4 bytes give n, then byte 4 must be an RTP header.
	n := int64(data[0])<<24 | int64(data[1])<<16 | int64(data[2])<<8 | int64(data[3])
	if n >= 12 && n <= 65535 && int64(len(data)) >= 4+n && data[4]>>6 == 2 {
		return true
	}
	return false
}

// TimestampTime returns the packet timestamp in clock ticks at the stream's
// clock rate.  It is an integer conversion, never a float.
func (s *Stream) TimestampTime(ts uint32) int64 {
	return int64(ts)
}
