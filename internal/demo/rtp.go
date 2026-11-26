package demo

import (
	"encoding/binary"

	"github.com/pion/rtp"
)

// PacketizeOpus wraps raw Opus packets in RTP using a 48 kHz clock and
// 20 ms timestamp steps. The caller controls which sequence numbers are
// present so loss and duplication can be simulated.
type OpusRTPWriter struct {
	ssrc      uint32
	clockStep uint32 // timestamp increment per packet (960 for 20 ms)
	nextSeq   uint16
	nextTS    uint32
}

// NewOpusRTPWriter builds a writer starting at seq/ts zero.
func NewOpusRTPWriter() *OpusRTPWriter {
	return &OpusRTPWriter{ssrc: 0x42424242, clockStep: 960}
}

// Write emits one RTP packet for an Opus payload at the given sequence
// number and its implied timestamp.
func (w *OpusRTPWriter) Write(seq uint16, payload []byte) ([]byte, error) {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 111, SequenceNumber: seq,
			Timestamp: uint32(seq) * w.clockStep, SSRC: w.ssrc,
		},
		Payload: payload,
	}
	return pkt.Marshal()
}

// PacketizeAAC wraps raw AAC access units in RFC 3640 generic AU mode:
// a 16-bit AU-headers-length, one 16-bit AU header per unit, then the
// units. Each RTP packet carries one access unit.
type AACRTPWriter struct {
	ssrc      uint32
	clockRate uint32
	step      uint32 // 1024 source samples per AAC frame
}

// NewAACRTPWriter builds a writer for the given native clock.
func NewAACRTPWriter(clockRate uint32) *AACRTPWriter {
	return &AACRTPWriter{ssrc: 0x41414141, clockRate: clockRate, step: 1024}
}

// Write emits one RFC 3640 RTP packet for a raw AAC access unit.
func (w *AACRTPWriter) Write(seq uint16, au []byte) ([]byte, error) {
	// AU-header for one unit: 16 bits, AU-index(3)=0, AU-size(13).
	auHeader := make([]byte, 2)
	binary.BigEndian.PutUint16(auHeader, uint16(len(au))&0x1fff)
	payload := []byte{0x00, 0x10} // AU-headers-length = 16 bits
	payload = append(payload, auHeader...)
	payload = append(payload, au...)
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 96, SequenceNumber: seq,
			Timestamp: uint32(seq) * w.step, SSRC: w.ssrc,
		},
		Payload: payload,
	}
	return pkt.Marshal()
}
