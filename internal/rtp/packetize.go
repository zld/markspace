package rtp

import (
	"github.com/pion/rtp"

	"mediacore/internal/clock"
)

// Packetize splits a payload into RTP packets with fixed payload size. The
// first packet carries an RTP header extension (id 0x0B, "keyframe") holding
// a single byte flag so a receiver can tell random access units apart without
// parsing the codec. Sequence numbers are assigned by the caller-supplied
// start value and wrap naturally.
func Packetize(payload []byte, payloadSize int, startSeq uint16, timestamp uint32, ssrc uint32, keyframe bool, markerLast bool) ([][]byte, error) {
	if payloadSize <= 0 {
		return nil, ErrZeroPayloadSize
	}
	var out [][]byte
	seq := startSeq
	for off := 0; off < len(payload); off += payloadSize {
		end := off + payloadSize
		if end > len(payload) {
			end = len(payload)
		}
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: seq,
				Timestamp:      timestamp,
				SSRC:           ssrc,
				Marker:         markerLast && end == len(payload),
			},
			Payload: payload[off:end],
		}
		if off == 0 && keyframe {
			flag := []byte{1}
			if err := pkt.SetExtension(0x0B, flag); err != nil {
				return nil, err
			}
		}
		b, err := pkt.Marshal()
		if err != nil {
			return nil, err
		}
		out = append(out, b)
		seq++
	}
	return out, nil
}

// IsKeyframePacket reports whether an RTP packet carries the project's
// keyframe header extension. Malformed packets return false.
func IsKeyframePacket(raw []byte) bool {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		return false
	}
	ext := pkt.GetExtension(0x0B)
	return len(ext) > 0 && ext[0] == 1
}

// Arrival builds a deterministic arrival time for the nth packet of a stream.
// Real networks jitter; the demo uses a fixed spacing plus a caller supplied
// offset so replays are byte-identical.
func Arrival(index int, spacing clock.Time, offset clock.Time) clock.Time {
	return clock.Time(index)*spacing + offset
}

// ErrZeroPayloadSize is returned by Packetize for a non-positive payload size.
var ErrZeroPayloadSize = errZero("rtp: payload size must be positive")

type errZero string

func (e errZero) Error() string { return string(e) }
