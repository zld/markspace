// Package ts parses MPEG-Transport-Stream packets, continuity counters and
// PSI tables (PAT/PMT).
package ts

import (
	"errors"
	"fmt"

	"mediarun/internal/clock"
)

const (
	// SyncByte marks the start of every MPEG-TS packet.
	SyncByte byte = 0x47
	// PacketSize is the only packet length this kernel accepts.
	PacketSize = 188
)

// Adaptation carries the parsed adaptation field of one packet.
type Adaptation struct {
	Length       int
	HasPCR       bool
	PCRBase      uint64 // 33-bit, 90 kHz
	PCRExt       uint16 // 9-bit, 27 MHz extension
	RandomAccess bool
}

// Packet is a parsed 188-byte MPEG-TS packet.
type Packet struct {
	PID              uint16
	CC               uint8
	PayloadUnitStart bool
	HasAdaptation    bool
	HasPayload       bool
	Adaptation       Adaptation
	Payload          []byte
	Index            int   // packet ordinal in the input stream
	Offset           int64 // byte offset of the sync byte
}

// ErrSyncByte is returned when the 0x47 sync byte is missing.
var ErrSyncByte = errors.New("missing 0x47 sync byte")

// ParsePacket validates and parses exactly one 188-byte packet.
func ParsePacket(buf []byte, index int, offset int64) (*Packet, error) {
	if len(buf) != PacketSize {
		return nil, fmt.Errorf("packet %d: length %d is not %d", index, len(buf), PacketSize)
	}
	if buf[0] != SyncByte {
		return nil, fmt.Errorf("%w: got 0x%02x at offset %d", ErrSyncByte, buf[0], offset)
	}
	tei := buf[1]&0x80 != 0
	pus := buf[1]&0x40 != 0
	prio := buf[1]&0x20 != 0
	pid := uint16(buf[1]&0x1f)<<8 | uint16(buf[2])
	afc := (buf[3] >> 4) & 0x03
	cc := buf[3] & 0x0f
	_ = tei
	_ = prio
	p := &Packet{
		PID:              pid,
		CC:               cc,
		PayloadUnitStart: pus,
		HasAdaptation:    afc == 2 || afc == 3,
		HasPayload:       afc == 1 || afc == 3,
		Index:            index,
		Offset:           offset,
	}
	cursor := 4
	if p.HasAdaptation {
		al := int(buf[4])
		cursor = 5
		if al > PacketSize-5 {
			return nil, fmt.Errorf("packet %d pid=0x%x: adaptation length %d exceeds packet", index, pid, al)
		}
		a := Adaptation{Length: al}
		if al > 0 {
			if 5+al > PacketSize {
				return nil, fmt.Errorf("packet %d pid=0x%x: adaptation field truncated", index, pid)
			}
			flags := buf[5]
			a.RandomAccess = flags&0x40 != 0
			if flags&0x10 != 0 { // PCR flag
				if al < 7 {
					return nil, fmt.Errorf("packet %d pid=0x%x: PCR flag set but field too short", index, pid)
				}
				b := buf[6:12]
				a.HasPCR = true
				a.PCRBase = uint64(b[0])<<25 | uint64(b[1])<<17 | uint64(b[2])<<9 |
					uint64(b[3])<<1 | uint64(b[4])>>7
				a.PCRExt = uint16(b[4]&0x01)<<8 | uint16(b[5])
			}
		}
		p.Adaptation = a
		cursor = 5 + al
	}
	if p.HasPayload && cursor < PacketSize {
		p.Payload = buf[cursor:]
	}
	return p, nil
}

// SplitStream cuts a byte stream into packets. Sync-byte and length failures
// become recorder events; a trailing partial packet is reported as truncated
// with its exact byte range. Resync scans forward to the next sync byte.
func SplitStream(data []byte, rec *clock.Recorder) []*Packet {
	var out []*Packet
	offset := int64(0)
	index := 0
	for len(data) > 0 {
		if len(data) < PacketSize {
			rec.Add(clock.Event{
				Kind: "truncated", Severity: "error", Track: "unknown",
				Range:  clock.ByteRange{Start: offset, End: offset + int64(len(data))},
				Detail: fmt.Sprintf("%d trailing bytes do not form a packet", len(data)),
			})
			break
		}
		if data[0] != SyncByte {
			next := indexOfSync(data)
			span := PacketSize
			if next > 0 {
				span = next
			}
			rec.Add(clock.Event{
				Kind: "sync_error", Severity: "error", Track: "unknown",
				Range:  clock.ByteRange{Start: offset, End: offset + int64(span)},
				Detail: "invalid sync byte; resynchronizing",
			})
			data = data[span:]
			offset += int64(span)
			continue
		}
		p, err := ParsePacket(data[:PacketSize], index, offset)
		if err != nil {
			rec.Add(clock.Event{
				Kind: "adaptation_error", Severity: "error",
				Range:  clock.ByteRange{Start: offset, End: offset + PacketSize},
				Detail: err.Error(),
			})
		} else {
			out = append(out, p)
			index++
		}
		data = data[PacketSize:]
		offset += PacketSize
	}
	return out
}

func indexOfSync(data []byte) int {
	for i := 1; i < len(data); i++ {
		if data[i] == SyncByte {
			return i
		}
	}
	return len(data)
}
