package fixture

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"

	"replaycheck/internal/clock"
)

// RTPMagic identifies the tiny length-prefixed RTP fixture container.
const RTPMagic = "RTPFIX1\n"

// MaxRTPPacketBytes bounds one fixture RTP record.
const MaxRTPPacketBytes = 8192

// RTPRecordSpec is one generated record. If Raw is non-nil it is used as a
// deliberately corrupt packet body.
type RTPRecordSpec struct {
	Sequence  uint16
	Timestamp uint32
	SSRC      uint32
	Payload   []byte
	Raw       []byte
}

// RTPPacket is a parsed RTP packet plus its original file offset.
type RTPPacket struct {
	clock.Stamp
	Packet *rtp.Packet
	Raw    []byte
}

// BuildRTPFile builds magic + uint32-length-prefixed RTP packets. It also
// returns the byte offset of each RTP packet body.
func BuildRTPFile(records []RTPRecordSpec) ([]byte, []int64, error) {
	out := []byte(RTPMagic)
	offsets := make([]int64, 0, len(records))
	for i, rec := range records {
		raw := rec.Raw
		if raw == nil {
			pkt := rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					Marker:         i == len(records)-1,
					PayloadType:    96,
					SequenceNumber: rec.Sequence,
					Timestamp:      rec.Timestamp,
					SSRC:           rec.SSRC,
				},
				Payload: rec.Payload,
			}
			var err error
			raw, err = pkt.Marshal()
			if err != nil {
				return nil, nil, fmt.Errorf("marshal RTP record %d: %w", i, err)
			}
		}
		if len(raw) > MaxRTPPacketBytes {
			return nil, nil, clock.NewError(clock.CodeTooLarge, "RTP fixture packet too large", -1)
		}
		length := make([]byte, 4)
		binary.BigEndian.PutUint32(length, uint32(len(raw)))
		out = append(out, length...)
		offsets = append(offsets, int64(len(out)))
		out = append(out, raw...)
	}
	return out, offsets, nil
}

// ParseRTPBytes parses the length-prefixed fixture and every RTP packet with
// pion/rtp. A corrupt packet returns prior packets plus a located Error.
func ParseRTPBytes(data []byte) ([]RTPPacket, error) {
	if len(data) < len(RTPMagic) || string(data[:len(RTPMagic)]) != RTPMagic {
		return nil, clock.NewError(clock.CodeBadHeader, "missing RTP fixture magic", 0)
	}
	packets := []RTPPacket{}
	pos := int64(len(RTPMagic))
	for pos < int64(len(data)) {
		if pos+4 > int64(len(data)) {
			return packets, clock.NewError(clock.CodeTruncated, "RTP record length prefix is truncated", pos)
		}
		n := int64(binary.BigEndian.Uint32(data[pos : pos+4]))
		packetStart := pos + 4
		packetEnd := packetStart + n
		if n <= 0 || n > MaxRTPPacketBytes {
			return packets, clock.NewError(clock.CodeCorruptPacket,
				fmt.Sprintf("invalid RTP record length %d", n), packetStart)
		}
		if packetEnd > int64(len(data)) {
			ce := clock.NewError(clock.CodeTruncated,
				fmt.Sprintf("RTP record of %d bytes ends beyond file at %d", n, packetEnd), packetStart)
			ce.EndOffset = int64(len(data))
			return packets, ce
		}
		raw := data[packetStart:packetEnd]
		if len(raw) < 12 || raw[0]>>6 != 2 {
			ce := clock.NewError(clock.CodeCorruptPacket,
				"RTP header is too short or has unsupported version", packetStart)
			ce.EndOffset = packetEnd
			return packets, ce
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(raw); err != nil {
			ce := clock.NewError(clock.CodeCorruptPacket,
				"pion/rtp unmarshal failed: "+err.Error(), packetStart)
			ce.EndOffset = packetEnd
			return packets, ce
		}
		packets = append(packets, RTPPacket{
			Stamp: clock.Stamp{
				Raw: pkt.Timestamp, Sequence: pkt.SequenceNumber, Offset: packetStart,
			},
			Packet: pkt, Raw: raw,
		})
		pos = packetEnd
	}
	return packets, nil
}

// Stamps converts parsed packets to clock unwrap inputs.
func Stamps(packets []RTPPacket) []clock.Stamp {
	out := make([]clock.Stamp, len(packets))
	for i, p := range packets {
		out[i] = p.Stamp
	}
	return out
}
