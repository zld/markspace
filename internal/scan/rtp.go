package scan

import (
	"encoding/binary"
	"io"
	"os"

	"github.com/pion/rtp/v2"
)

// RTPBoundary summarizes the cross-node transmission record of one
// representation. Sequence numbers and timestamps come straight from the
// captured RTP headers via pion/rtp; no reordering or repair is attempted.
type RTPBoundary struct {
	PacketCount        int
	FirstSequence      uint16
	LastSequence       uint16
	FirstTimestamp     uint32
	LastTimestamp      uint32
	// SequenceGaps lists the missing RTP sequence numbers (expected values
	// that never arrived).
	SequenceGaps []uint16
	// Markers maps each packet's input index to the mfhd number carried in
	// its payload marker.
	Markers map[uint32]uint32
}

// ReadRTPStream decodes the length-prefixed RTP stream written by the
// fixture generator.
func ReadRTPStream(path string) (*RTPBoundary, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	boundary := &RTPBoundary{Markers: map[uint32]uint32{}}
	var prevSeq uint16
	first := true
	for {
		var lenBuf [2]byte
		if _, err := io.ReadFull(f, lenBuf[:]); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		frameLen := binary.BigEndian.Uint16(lenBuf[:])
		blob := make([]byte, frameLen)
		if _, err := io.ReadFull(f, blob); err != nil {
			return nil, err
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(blob); err != nil {
			return nil, err
		}
		if first {
			boundary.FirstSequence = pkt.SequenceNumber
			boundary.FirstTimestamp = pkt.Timestamp
			first = false
		} else {
			for want := prevSeq + 1; want != pkt.SequenceNumber; want++ {
				boundary.SequenceGaps = append(boundary.SequenceGaps, want)
			}
		}
		prevSeq = pkt.SequenceNumber
		boundary.LastSequence = pkt.SequenceNumber
		boundary.LastTimestamp = pkt.Timestamp
		boundary.PacketCount++
		if len(pkt.Payload) >= 8 {
			inputIdx := binary.BigEndian.Uint32(pkt.Payload[0:4])
			mfhd := binary.BigEndian.Uint32(pkt.Payload[4:8])
			boundary.Markers[inputIdx] = mfhd
		}
	}
	return boundary, nil
}
