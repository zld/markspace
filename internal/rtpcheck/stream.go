// Package rtpcheck validates RTP packet boundaries and extends 32-bit
// timestamps across wrap-around before they participate in track boundary
// checks. Parsing uses github.com/pion/rtp; clock unwrapping lives in the
// clock package and is applied to the parsed timestamps here.
package rtpcheck

import (
	"bytes"
	"fmt"

	"github.com/pion/rtp"

	"mediascan/internal/clock"
	"mediascan/internal/errs"
)

// MaxPacketSize bounds a single RTP packet; larger inputs are rejected.
const MaxPacketSize = 9000

// Packet is the normalized view of one validated RTP packet.
type Packet struct {
	Sequence     uint16
	Timestamp32  uint32 // raw RTP timestamp
	TimestampExt uint64 // unwrapped absolute timestamp
	Marker       bool
	PayloadType  uint8
	SSRC         uint32
	PayloadLen   int
	ByteOffset   int64  // offset of the packet inside the source bytes
	Raw          []byte // exact original datagram bytes
}

// Stream is one SSRC's ordered, unwrapped packet stream.
type Stream struct {
	SSRC      uint32
	ClockRate uint32
	Packets   []Packet
	unwrapper clock.RTPUnwrapper
	lastSeq   uint16
	haveSeq   bool
}

// ParseStream parses length-prefixed RTP packets: each datagram is
// preceded by a 2-byte big-endian length (the project's RTP-over-file
// framing). It enforces version 2, sequence monotonicity, timestamp
// unwrap and payload bounds.
func ParseStream(source string, data []byte, clockRate uint32) (*Stream, error) {
	if !clock.ValidScale(clockRate) {
		return nil, errs.New(errs.KindRTP, source, errs.Unknown, errs.Unknown, 0,
			"invalid RTP clock rate %d", clockRate)
	}
	s := &Stream{ClockRate: clockRate}
	var offset int64
	index := int64(0)
	for len(data) > 0 {
		if len(data) < 2 {
			return nil, errs.New(errs.KindRTP, source, offset, index, 0,
				"truncated length prefix at offset %d", offset)
		}
		length := int(data[0])<<8 | int(data[1])
		if length < 12 || length > MaxPacketSize {
			return nil, errs.New(errs.KindRTP, source, offset, index, 0,
				"packet %d declares invalid length %d", index, length)
		}
		if len(data) < 2+length {
			return nil, errs.New(errs.KindRTP, source, offset, index, 0,
				"packet %d needs %d bytes but only %d remain",
				index, length, len(data)-2)
		}
		raw := data[2 : 2+length]
		var p rtp.Packet
		if err := p.Unmarshal(raw); err != nil {
			return nil, errs.New(errs.KindRTP, source, offset+2, index, 0,
				"packet %d unmarshal failed: %v", index, err)
		}
		if p.Version != 2 {
			return nil, errs.New(errs.KindRTP, source, offset+2, index, 0,
				"packet %d has RTP version %d, want 2", index, p.Version)
		}
		if err := s.accept(&p, raw, offset+2, index); err != nil {
			return nil, err
		}
		data = data[2+length:]
		offset += 2 + int64(length)
		index++
	}
	if len(s.Packets) == 0 {
		return nil, errs.New(errs.KindRTP, source, 0, errs.Unknown, 0,
			"no RTP packets found")
	}
	return s, nil
}

// EncodeDatagram wraps one RTP datagram in the 2-byte length framing.
func EncodeDatagram(p *rtp.Packet) ([]byte, error) {
	raw, err := p.Marshal()
	if err != nil {
		return nil, err
	}
	if len(raw) > 0xFFFF {
		return nil, fmt.Errorf("rtp datagram %d bytes exceeds 65535 framing limit", len(raw))
	}
	out := make([]byte, 2+len(raw))
	out[0] = byte(len(raw) >> 8)
	out[1] = byte(len(raw))
	copy(out[2:], raw)
	return out, nil
}

func (s *Stream) accept(p *rtp.Packet, raw []byte, offset, index int64) error {
	if s.SSRC == 0 && len(s.Packets) == 0 {
		s.SSRC = p.SSRC
	} else if p.SSRC != s.SSRC {
		return errs.New(errs.KindRTP, "", offset, index, 0,
			"SSRC changed from %d to %d within one stream", s.SSRC, p.SSRC)
	}
	if s.haveSeq {
		if p.SequenceNumber != s.lastSeq+1 {
			return errs.New(errs.KindRTP, "", offset, index, 0,
				"sequence gap: expected %d got %d", s.lastSeq+1, p.SequenceNumber)
		}
	}
	s.lastSeq = p.SequenceNumber
	s.haveSeq = true
	ext := s.unwrapper.Extend(p.Timestamp)
	if n := len(s.Packets); n > 0 && ext < s.Packets[n-1].TimestampExt {
		return errs.New(errs.KindRTP, "", offset, index, 0,
			"unwrapped timestamp regressed")
	}
	s.Packets = append(s.Packets, Packet{
		Sequence:     p.SequenceNumber,
		Timestamp32:  p.Timestamp,
		TimestampExt: ext,
		Marker:       p.Marker,
		PayloadType:  p.PayloadType,
		SSRC:         p.SSRC,
		PayloadLen:   len(p.Payload),
		ByteOffset:   offset,
		Raw:          append([]byte(nil), raw...),
	})
	return nil
}

// VerifyBoundary checks that the stream's unwrapped end timestamp matches
// the container track's expected duration on its own clock rate, within
// the supplied millisecond tolerance (integer cross-multiplication).
func (s *Stream) VerifyBoundary(source string, expectedTicks int64, toleranceMS int64) error {
	last := s.Packets[len(s.Packets)-1]
	ok, err := clock.WithinTolerance(
		int64(last.TimestampExt), s.ClockRate,
		expectedTicks, s.ClockRate, toleranceMS)
	if err != nil {
		return errs.New(errs.KindRTP, source, last.ByteOffset,
			int64(len(s.Packets)-1), 0, "boundary check failed: %v", err)
	}
	if !ok {
		return errs.New(errs.KindTimeline, source, last.ByteOffset,
			int64(len(s.Packets)-1), 0,
			"RTP end timestamp %d differs from track boundary %d by more than %dms",
			last.TimestampExt, expectedTicks, toleranceMS)
	}
	return nil
}

// MarshalRoundTrip re-parses each stored datagram and re-serializes it,
// confirming the packet boundary and header fields are preserved exactly.
func (s *Stream) MarshalRoundTrip(source string) error {
	for _, pk := range s.Packets {
		var p rtp.Packet
		if err := p.Unmarshal(pk.Raw); err != nil {
			return errs.New(errs.KindRTP, source, pk.ByteOffset,
				int64(pk.Sequence), 0, "round-trip unmarshal failed: %v", err)
		}
		out, err := p.Marshal()
		if err != nil {
			return errs.New(errs.KindRTP, source, pk.ByteOffset,
				int64(pk.Sequence), 0, "round-trip marshal failed: %v", err)
		}
		if !bytes.Equal(out, pk.Raw) {
			return errs.New(errs.KindRTP, source, pk.ByteOffset,
				int64(pk.Sequence), 0,
				"round-trip bytes differ: %d -> %d", len(pk.Raw), len(out))
		}
	}
	return nil
}
