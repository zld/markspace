// Package ts parses MPEG-TS 188-byte transport packets, tracks per-PID
// continuity counters and adaptation fields, and assembles PSI sections.
package ts

import (
	"errors"
	"io"

	"tsrepair/internal/mediaerr"
)

// PacketSize is the fixed MPEG-TS transport packet length.
const PacketSize = 188

// SyncByte is the 0x47 resynchronisation marker.
const SyncByte byte = 0x47

// Well-known PIDs.
const (
	PIDPAT  uint16 = 0x0000
	PIDNull uint16 = 0x1FFF
)

// Adaptation field control values (the 2-bit adaptation_field_control).
const (
	AFCReserved       = 0
	AFCPayloadOnly    = 1
	AFCAdaptationOnly = 2
	AFCPayloadAndAF   = 3
)

// MaxSectionLength bounds PSI section assembly (ISO 13818-1 allows 1021).
const MaxSectionLength = 1024

// Packet is a parsed transport packet. Payload aliases an internal buffer and
// remains valid until the next ReadPacket call.
type Packet struct {
	Offset            int64 // byte offset of the sync byte in the input
	PID               uint16
	TEI               bool  // transport error indicator
	PUSI              bool  // payload unit start indicator
	Priority          bool
	AdaptationControl uint8
	CC                uint8
	AdaptationLength  int // -1 when the packet carries no adaptation field
	RandomAccess      bool
	HasPCR            bool
	PCRBase           int64
	PCRExt            int64
	Payload           []byte
	Raw               []byte
}

// PCRRaw combines the 33-bit base and 9-bit extension into a 42-bit value in
// 27 MHz ticks.
func (p *Packet) PCRRaw() int64 { return p.PCRBase*300 + p.PCRExt }

// Reader streams 188-byte packets from an io.Reader, enforcing sync and
// per-PID continuity semantics. A small internal buffer makes
// resynchronisation possible without seek support; per-packet memory is
// bounded regardless of stream length.
type Reader struct {
	r          io.Reader
	buf        []byte // buffered, not yet consumed bytes
	nextOffset int64  // input offset of buf[0]
	cc         map[uint16]*pidState
	ec         *mediaerr.Collector
	// ResyncScan limits how far the reader scans for a sync byte after loss.
	ResyncScan int64
}

type pidState struct {
	lastCC    uint8
	haveCC    bool
	lastPktNo int64
}

// NewReader wraps r. Recoverable problems are reported through ec; fatal
// problems are returned as errors.
func NewReader(r io.Reader, ec *mediaerr.Collector) *Reader {
	return &Reader{
		r:          r,
		cc:         map[uint16]*pidState{},
		ec:         ec,
		ResyncScan: 64 * PacketSize,
	}
}

// ErrResync is returned when no valid sync marker exists in the scan window.
var ErrResync = errors.New("ts: sync byte not found within rescan window")

// fill ensures at least n bytes are buffered, reading more as needed. It
// returns io.EOF only when the underlying source is exhausted and fewer than
// n bytes are available.
func (r *Reader) fill(n int) error {
	for len(r.buf) < n {
		tmp := make([]byte, 4096)
		got, err := r.r.Read(tmp)
		r.buf = append(r.buf, tmp[:got]...)
		if err != nil {
			if len(r.buf) >= n {
				return nil
			}
			return err
		}
	}
	return nil
}

// ReadPacket returns the next packet. At clean EOF it returns io.EOF.
func (r *Reader) ReadPacket() (*Packet, error) {
	if err := r.fill(PacketSize); err != nil {
		return nil, err
	}
	if r.buf[0] != SyncByte {
		r.ec.Add(mediaerr.Recoverable(mediaerr.CodeSyncLost, 0, r.nextOffset,
			"expected 0x47 sync byte", "rescan for next sync marker"))
		if !r.resync() {
			return nil, mediaerr.Fatal(mediaerr.CodeSyncLost, 0, r.nextOffset,
				"no sync marker within rescan window", "abort stream")
		}
	}

	raw := r.buf[:PacketSize:PacketSize]
	off := r.nextOffset
	p, perr := parsePacket(raw, off)
	if perr != nil {
		r.ec.Add(perr)
	}
	// A structurally invalid packet is still skipped as one 188-byte unit,
	// never silently treated as continuous data.
	r.buf = r.buf[PacketSize:]
	r.nextOffset += PacketSize
	if p != nil && p.PID != PIDNull {
		r.checkCC(p)
	}
	return p, nil
}

// resync advances the buffer to the next 0x47 that is confirmed by another
// 0x47 exactly one packet later. Returns false when the window is exhausted.
func (r *Reader) resync() bool {
	// Candidate sync positions run up to one packet beyond ResyncScan; the
	// confirming marker then sits one more packet on.
	limit := int(r.ResyncScan) + 1
	var fallback int = -1
	for i := 1; i < limit; i++ {
		if err := r.fill(i + 1); err != nil {
			if len(r.buf) < i+1 {
				break // source exhausted
			}
		}
		if r.buf[i] != SyncByte {
			continue
		}
		if fallback < 0 {
			fallback = i
		}
		need := i + PacketSize + 1
		if err := r.fill(need); err != nil {
			if len(r.buf) < need {
				// Stream ends before the confirming marker: accept the lone
				// sync byte at end-of-stream, otherwise keep scanning.
				continue
			}
		}
		if r.buf[i+PacketSize] == SyncByte {
			r.nextOffset += int64(i)
			r.buf = r.buf[i:]
			return true
		}
	}
	if fallback >= 0 {
		r.nextOffset += int64(fallback)
		r.buf = r.buf[fallback:]
		return true
	}
	return false
}
