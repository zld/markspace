// Package ts parses MPEG-TS at the packet level: 188-byte scanning,
// adaptation/PCR extraction, PSI tables and continuity counters. It produces
// structured events against the project-wide clock error model rather than
// repairing streams silently.
package ts

import (
	"bufio"
	"fmt"
	"io"

	"mediad/internal/clock"
)

// PacketSize is the fixed MPEG-TS packet length in bytes.
const PacketSize = 188

// SyncByte is the mandatory first byte of every TS packet.
const SyncByte = 0x47

// MaxInputSize is the 2 GB single-input limit from the project boundary.
const MaxInputSize int64 = 2 * 1024 * 1024 * 1024

// Well-known PIDs.
const (
	PIDPAT  = 0x0000
	PIDNull = 0x1fff
)

// Packet is one parsed 188-byte MPEG-TS packet.
type Packet struct {
	Offset          int64
	TEI             bool
	PUSI            bool
	Priority        bool
	PID             uint16
	Scrambling      uint8
	Adaptation      uint8 // 0 none, 1 adaptation only, 2 payload only, 3 both
	CC              uint8
	AdaptationBytes []byte
	Payload         []byte
}

// HasAdaptation reports whether an adaptation field is present
// (adaptation_control 10 = adaptation only, 11 = adaptation + payload).
func (p Packet) HasAdaptation() bool { return p.Adaptation == 2 || p.Adaptation == 3 }

// HasPayload reports whether a payload section is present
// (adaptation_control 01 = payload only, 11 = adaptation + payload).
func (p Packet) HasPayload() bool { return p.Adaptation == 1 || p.Adaptation == 3 }

// PCR returns the 33-bit PCR base in 90 kHz ticks and true when the
// adaptation field carries a PCR (PCR flag set, length sufficient).
// AdaptationBytes excludes the one-byte adaptation length: a[0] is the
// flags byte and the six PCR bytes start at a[1].
func (p Packet) PCR() (base int64, ok bool) {
	a := p.AdaptationBytes
	if len(a) < 7 || a[0]&0x10 == 0 {
		return 0, false
	}
	b := a[1:7]
	v := uint64(b[0])<<25 | uint64(b[1])<<17 | uint64(b[2])<<9 |
		uint64(b[3])<<1 | uint64(b[4])>>7
	return int64(v), true
}

// Parse decodes one 188-byte packet. b must be exactly PacketSize bytes.
func Parse(b []byte, offset int64) (Packet, error) {
	if len(b) != PacketSize {
		return Packet{}, fmt.Errorf("ts: packet at %d has %d bytes, want %d", offset, len(b), PacketSize)
	}
	if b[0] != SyncByte {
		return Packet{}, clock.RangeError{
			Kind: clock.KindSync, Start: offset, End: offset + PacketSize,
			Msg: fmt.Sprintf("missing 0x47 sync byte, got 0x%02x", b[0]),
		}
	}
	p := Packet{
		Offset:     offset,
		TEI:        b[1]&0x80 != 0,
		PUSI:       b[1]&0x40 != 0,
		Priority:   b[1]&0x20 != 0,
		PID:        uint16(b[1]&0x1f)<<8 | uint16(b[2]),
		Scrambling: b[3] >> 6,
		Adaptation: b[3] >> 4 & 0x03,
		CC:         b[3] & 0x0f,
	}
	pos := 4
	if p.HasAdaptation() {
		al := int(b[pos])
		pos++
		if al > PacketSize-pos {
			return Packet{}, clock.RangeError{
				Kind: clock.KindSync, Start: offset, End: offset + PacketSize,
				Msg: fmt.Sprintf("adaptation length %d overruns packet", al),
			}
		}
		if al > 0 {
			end := pos + al
			p.AdaptationBytes = b[pos:end]
			pos = end
		}
	}
	if p.HasPayload() && pos < PacketSize {
		p.Payload = b[pos:PacketSize]
	}
	return p, nil
}

// Event is emitted by Scan for every packet and every resync point.
type Event struct {
	Pkt       Packet
	SyncError *clock.RangeError // set for skipped non-sync runs
}

// ScanOptions bounds the stream; zero values select defaults.
type ScanOptions struct {
	MaxSize int64
}

// Scan reads 188-byte packets until EOF. Non-sync bytes trigger resync: the
// skipped run is reported as a sync error and scanning resumes at the next
// 0x47. A trailing partial packet is reported as truncation with its offset.
// Inputs beyond MaxSize are rejected before any packet is emitted.
func Scan(r io.Reader, opts ScanOptions, emit func(Event) error) error {
	max := opts.MaxSize
	if max <= 0 {
		max = MaxInputSize
	}
	br := bufio.NewReaderSize(r, PacketSize*64)
	var offset, skipped int64
	syncStart := int64(-1)
	buf := make([]byte, PacketSize)
	for {
		if offset >= max {
			return fmt.Errorf("ts: input exceeds %d byte limit", max)
		}
		b, err := br.ReadByte()
		if err == io.EOF {
			if syncStart >= 0 {
				return clock.RangeError{
					Kind: clock.KindTruncated, Start: syncStart, End: offset,
					Msg: "stream ended while searching for sync byte",
				}
			}
			return nil
		}
		if err != nil {
			return err
		}
		if b != SyncByte {
			if syncStart < 0 {
				syncStart = offset
			}
			skipped++
			offset++
			continue
		}
		if syncStart >= 0 {
			if err := emit(Event{SyncError: &clock.RangeError{
				Kind: clock.KindSync, Start: syncStart, End: offset,
				Msg: fmt.Sprintf("resynced after %d non-sync bytes", skipped),
			}}); err != nil {
				return err
			}
			syncStart, skipped = -1, 0
		}
		// The sync byte is already consumed; read the remaining 187 bytes.
		buf[0] = SyncByte
		n, err := io.ReadFull(br, buf[1:])
		if err != nil {
			return clock.RangeError{
				Kind: clock.KindTruncated, Start: offset, End: offset + 1 + int64(n),
				Msg: fmt.Sprintf("trailing %d bytes do not form a 188-byte packet", 1+n),
			}
		}
		pkt, perr := Parse(buf, offset)
		if perr != nil {
			return perr
		}
		if err := emit(Event{Pkt: pkt}); err != nil {
			return err
		}
		offset += PacketSize
	}
}
