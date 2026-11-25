package fixture

import (
	"encoding/binary"
	"os"

	"github.com/pion/rtp"
)

// RTPSpec describes a deterministic RTP capture.
type RTPSpec struct {
	SSRC       uint32
	ClockRate  uint32
	PacketCount uint32
	TimestampStep uint32 // ticks between consecutive packets
	PayloadSize int
	Key         byte
	// Wrap emits the first packets near 2^32 so the timestamp counter wraps.
	Wrap bool
	// BoundaryJump resets the raw timestamp close to zero at packet index
	// JumpIndex, simulating a source switch.
	BoundaryJump bool
	JumpIndex    uint32
}

// WriteRTP renders a length-prefixed RTP capture file.
func WriteRTP(path string, spec RTPSpec) error {
	data, err := BuildRTP(spec)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// BuildRTP returns the framed capture bytes: repeating u32 length + packet.
func BuildRTP(spec RTPSpec) ([]byte, error) {
	startTS := uint32(0)
	if spec.Wrap {
		startTS = uint32(0xFFFFFFFF) - spec.TimestampStep*2
	}
	var out []byte
	ts := startTS
	for i := uint32(0); i < spec.PacketCount; i++ {
		rawTS := ts
		if spec.BoundaryJump && i == spec.JumpIndex {
			rawTS = 100 // clock reset after source switch
		}
		pkt := rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(i),
				Timestamp:      rawTS,
				SSRC:           spec.SSRC,
				Marker:         i%5 == 4,
			},
			Payload: deterministicBytes(spec.Key+byte(i), spec.PayloadSize),
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return nil, err
		}
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(raw)))
		out = append(out, lenBuf[:]...)
		out = append(out, raw...)
		if spec.BoundaryJump && i == spec.JumpIndex {
			// Keep advancing the local clock as if nothing happened; the
			// normalizer is expected to flag the raw reset instead.
			ts += spec.TimestampStep
			continue
		}
		ts += spec.TimestampStep
	}
	return out, nil
}

// TruncateFile cuts data at byteAt and writes the shortened file, producing
// a cross-box truncation fault when byteAt lands inside a box/packet body.
func TruncateFile(path string, data []byte, byteAt int) error {
	if byteAt > len(data) {
		byteAt = len(data)
	}
	return os.WriteFile(path, data[:byteAt], 0o644)
}
