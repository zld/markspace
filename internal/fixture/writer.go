// Package fixture produces deterministic, hand-verifiable MPEG-TS sample
// streams. Every emitted packet is exactly 188 bytes and the layout of each
// scenario is recorded in a Manifest so reports can be cross-checked packet
// by packet without relying on any external capture.
package fixture

import (
	"fmt"
	"io"

	"tsrebuild/internal/tsreader"
)

// PacketWriter emits 188-byte TS packets while maintaining one 4-bit
// continuity counter per PID.
type PacketWriter struct {
	w     io.Writer
	cc    map[uint16]uint8
	count int64
}

// NewPacketWriter creates a writer targeting w.
func NewPacketWriter(w io.Writer) *PacketWriter {
	return &PacketWriter{w: w, cc: make(map[uint16]uint8)}
}

// PacketCount returns the number of packets written so far.
func (pw *PacketWriter) PacketCount() int64 { return pw.count }

// CC returns the next continuity counter that will be used for pid.
func (pw *PacketWriter) CC(pid uint16) uint8 { return pw.cc[pid] }

// SetCC forces the continuity counter of pid (used to inject a CC jump).
func (pw *PacketWriter) SetCC(pid uint16, cc uint8) { pw.cc[pid] = cc & 0x0f }

// writePacket serializes one packet. payload may be shorter than the 184-byte
// payload capacity; the remainder is stuffed with 0xFF.
func (pw *PacketWriter) writePacket(pid uint16, pusi bool, payload []byte, cc uint8) error {
	if len(payload) > tsreader.PacketSize-4 {
		return fmt.Errorf("fixture: payload %d exceeds %d bytes", len(payload), tsreader.PacketSize-4)
	}
	pkt := make([]byte, tsreader.PacketSize)
	pkt[0] = tsreader.SyncByte
	b1 := byte(pid>>8) & 0x1f
	if pusi {
		b1 |= 0x40
	}
	pkt[1] = b1
	pkt[2] = byte(pid)
	pkt[3] = 0x10 | (cc & 0x0f) // adaptation control 01: payload only
	copy(pkt[4:], payload)
	for i := 4 + len(payload); i < tsreader.PacketSize; i++ {
		pkt[i] = 0xFF
	}
	if _, err := pw.w.Write(pkt); err != nil {
		return err
	}
	pw.count++
	return nil
}

// WriteES emits one elementary-stream packet with the PID's next CC and
// returns its packet index and the CC used.
func (pw *PacketWriter) WriteES(pid uint16, payload []byte) (int64, uint8, error) {
	cc := pw.cc[pid]
	pw.cc[pid] = (cc + 1) & 0x0f
	return pw.count, cc, pw.writePacket(pid, false, payload, cc)
}

// WritePSI fragments a complete PSI section across one or more packets. The
// first packet carries PUSI and the section starts immediately after its
// zero pointer_field; when fragmentSize is below the section length the
// remaining bytes span continuation packets. It returns the packet indexes.
func (pw *PacketWriter) WritePSI(pid uint16, section []byte, fragmentSize int) ([]int64, error) {
	if fragmentSize <= 0 {
		fragmentSize = tsreader.PacketSize - 4
	}
	var indexes []int64
	off := 0
	first := true
	for off < len(section) || first {
		var payload []byte
		room := fragmentSize
		if first {
			payload = []byte{0x00} // pointer_field = 0, then the section begins
			room--
		}
		n := len(section) - off
		if n > room {
			n = room
		}
		payload = append(payload, section[off:off+n]...)
		cc := pw.cc[pid]
		pw.cc[pid] = (cc + 1) & 0x0f
		if err := pw.writePacket(pid, first, payload, cc); err != nil {
			return nil, err
		}
		indexes = append(indexes, pw.count-1)
		off += n
		first = false
		if n == 0 {
			break
		}
	}
	return indexes, nil
}
