package fixture

import (
	"bytes"
	"encoding/binary"

	"github.com/pion/rtp"
)

// TraceMagic prefixes every generated length-prefixed RTP trace.
var TraceMagic = [4]byte{'R', 'T', 'P', '1'}

// TSOverRTPPayloadType is the dynamic-but-fixed payload type for the traces.
const TSOverRTPPayloadType = 33

// RTPWriter wraps 188-byte TS packets into RFC 2250 MPEG-TS-over-RTP packets
// and serializes them with the RTP1 length-prefixed trace framing.
type RTPWriter struct {
	buf        bytes.Buffer
	ssrc       uint32
	seq        uint16
	rtpClock   uint32 // 90 kHz
	packets    int
	rtpRecords [][]byte // marshaled RTP packets, before framing
}

// NewRTPWriter opens a trace with the given SSRC and initial RTP timestamp.
func NewRTPWriter(ssrc uint32, startTimestamp uint32) *RTPWriter {
	w := &RTPWriter{ssrc: ssrc, rtpClock: startTimestamp}
	w.buf.Write(TraceMagic[:])
	return w
}

// PushTS groups TS packets into RTP payloads (multi-packet per RFC 2250) and
// emits one RTP packet. rtpStep advances the 90 kHz timestamp.
func (w *RTPWriter) PushTS(tsPackets [][]byte, rtpStep uint32) error {
	var payload bytes.Buffer
	for _, p := range tsPackets {
		payload.Write(p)
	}
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    TSOverRTPPayloadType,
			SequenceNumber: w.seq,
			Timestamp:      w.rtpClock,
			SSRC:           w.ssrc,
		},
		Payload: payload.Bytes(),
	}
	raw, err := pkt.Marshal()
	if err != nil {
		return err
	}
	w.rtpRecords = append(w.rtpRecords, raw)
	w.seq++
	w.rtpClock += rtpStep
	w.packets++
	return nil
}

// DuplicateLast appends a second copy of the most recent RTP packet. The
// reorder layer must count and drop it.
func (w *RTPWriter) DuplicateLast() {
	if len(w.rtpRecords) == 0 {
		return
	}
	w.rtpRecords = append(w.rtpRecords, w.rtpRecords[len(w.rtpRecords)-1])
}

// ReorderLastTwo swaps the final two RTP records (a late but in-window jitter).
func (w *RTPWriter) ReorderLastTwo() {
	n := len(w.rtpRecords)
	if n < 2 {
		return
	}
	w.rtpRecords[n-1], w.rtpRecords[n-2] = w.rtpRecords[n-2], w.rtpRecords[n-1]
}

// DropLast removes the most recent RTP record entirely (a lost packet).
func (w *RTPWriter) DropLast() {
	if len(w.rtpRecords) > 0 {
		w.rtpRecords = w.rtpRecords[:len(w.rtpRecords)-1]
	}
}

// DropAt removes one record at a fixed index and closes the gap, simulating a
// mid-stream loss while leaving the final record available for duplication.
func (w *RTPWriter) DropAt(index int) {
	if index < 0 || index >= len(w.rtpRecords) {
		return
	}
	w.rtpRecords = append(w.rtpRecords[:index], w.rtpRecords[index+1:]...)
}

// Frame serializes the (possibly mutated) records with length prefixes.
func (w *RTPWriter) Frame() []byte {
	var out bytes.Buffer
	out.Write(TraceMagic[:])
	var lenBuf [4]byte
	for _, rec := range w.rtpRecords {
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(rec)))
		out.Write(lenBuf[:])
		out.Write(rec)
	}
	return out.Bytes()
}

// Count reports how many RTP records are currently staged.
func (w *RTPWriter) Count() int { return len(w.rtpRecords) }
