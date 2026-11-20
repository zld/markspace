// Package ts implements a streaming MPEG-TS packet reader for 188-byte
// packets with loss-of-sync recovery.
package ts

import (
	"errors"
	"fmt"
	"io"
)

const (
	// PacketSize is the only supported MPEG-TS packet size.
	PacketSize = 188
	// SyncByte marks the start of every TS packet.
	SyncByte = 0x47
	// syncConfirm is how many consecutive well-spaced sync bytes are
	// required before a candidate position is accepted as synced.
	syncConfirm = 3
	// readChunk is the streaming read granularity.
	readChunk = PacketSize * 64
)

// ErrUnsupportedPacketSize is returned when the input looks like 192- or
// 204-byte encapsulated TS, which this tool does not support.
var ErrUnsupportedPacketSize = errors.New("ts: unsupported packet size (only 188-byte MPEG-TS is supported)")

// ResyncError reports that sync could not be recovered at Offset.
type ResyncError struct{ Offset int64 }

func (e *ResyncError) Error() string {
	return fmt.Sprintf("ts: cannot resynchronize at offset %d", e.Offset)
}

// Packet is one parsed 188-byte TS packet. Raw always holds the original
// bytes so callers (e.g. the slicer) can re-emit the packet.
type Packet struct {
	Offset                 int64 // absolute file offset of the packet
	PID                    uint16
	TEI                    bool
	PUSI                   bool
	ScramblingControl      byte // non-zero means encrypted payload (unsupported)
	AdaptationFieldControl byte // 1=payload, 2=adaptation only, 3=both
	ContinuityCounter      byte
	DiscontinuityIndicator bool
	HasPCR                 bool
	PCRBase                uint64 // 90kHz PCR base
	Payload                []byte // nil when the packet carries no payload
	Raw                    [PacketSize]byte
}

// HasAdaptation reports whether an adaptation field is present.
func (p *Packet) HasAdaptation() bool { return p.AdaptationFieldControl == 2 || p.AdaptationFieldControl == 3 }

// HasPayload reports whether a payload is present.
func (p *Packet) HasPayload() bool { return p.AdaptationFieldControl == 1 || p.AdaptationFieldControl == 3 }

// Reader streams packets from an io.Reader without loading the whole input.
type Reader struct {
	r        io.Reader
	buf      []byte // undelivered bytes, buf[0] is at absolute offset base
	base     int64
	eof      bool
	Trailing int // bytes left over at EOF that do not form a full packet
}

// NewReader wraps r. r is consumed incrementally.
func NewReader(r io.Reader) *Reader { return &Reader{r: r} }

// Next returns the next packet, or io.EOF at end of input. Trailing is set
// to the number of leftover bytes (<188) before io.EOF is returned.
func (r *Reader) Next() (*Packet, error) {
	for {
		if len(r.buf) >= PacketSize {
			if err := r.ensureSync(); err != nil {
				return nil, err
			}
			raw := r.buf[:PacketSize]
			pkt, err := parsePacket(raw, r.base)
			if err != nil {
				return nil, err
			}
			r.buf = r.buf[PacketSize:]
			r.base += PacketSize
			return pkt, nil
		}
		if r.eof {
			r.Trailing = len(r.buf)
			r.buf = nil
			return nil, io.EOF
		}
		if err := r.fill(); err != nil {
			return nil, err
		}
	}
}

func (r *Reader) fill() error {
	chunk := make([]byte, readChunk)
	n, err := r.r.Read(chunk)
	if n > 0 {
		r.buf = append(r.buf, chunk[:n]...)
	}
	if err == io.EOF {
		r.eof = true
		return nil
	}
	return err
}

// ensureSync verifies buf starts on a packet boundary, resynchronizing if
// possible. Bytes skipped during resync are discarded.
func (r *Reader) ensureSync() error {
	if r.buf[0] == SyncByte {
		return nil
	}
	// Detect unsupported 192/204-byte encapsulation: sync byte present but
	// the next sync arrives late with a fixed stride.
	for _, stride := range []int{192, 204} {
		if r.alignedAt(stride) {
			return ErrUnsupportedPacketSize
		}
	}
	limit := len(r.buf) - PacketSize*syncConfirm
	for i := 1; i <= limit; i++ {
		if r.buf[i] == SyncByte && r.confirmAt(i) {
			r.buf = r.buf[i:]
			r.base += int64(i)
			return nil
		}
	}
	if !r.eof {
		return nil // not enough buffered data yet; keep reading
	}
	return &ResyncError{Offset: r.base}
}

// alignedAt reports whether sync bytes repeat with the given stride.
func (r *Reader) alignedAt(stride int) bool {
	if r.buf[0] != SyncByte {
		return false
	}
	for k := 1; k <= syncConfirm; k++ {
		pos := k * stride
		if pos >= len(r.buf) {
			return false
		}
		if r.buf[pos] != SyncByte {
			return false
		}
	}
	return true
}

// confirmAt reports whether i is followed by syncConfirm-1 more sync bytes
// spaced exactly one packet apart.
func (r *Reader) confirmAt(i int) bool {
	for k := 1; k < syncConfirm; k++ {
		pos := i + k*PacketSize
		if pos >= len(r.buf) || r.buf[pos] != SyncByte {
			return false
		}
	}
	return true
}

func parsePacket(raw []byte, off int64) (*Packet, error) {
	if raw[0] != SyncByte {
		return nil, fmt.Errorf("ts: missing sync byte at offset %d", off)
	}
	p := &Packet{Offset: off}
	copy(p.Raw[:], raw)
	p.TEI = raw[1]&0x80 != 0
	p.PUSI = raw[1]&0x40 != 0
	p.PID = uint16(raw[1]&0x1f)<<8 | uint16(raw[2])
	p.ScramblingControl = raw[3] >> 6
	p.AdaptationFieldControl = (raw[3] >> 4) & 0x3
	p.ContinuityCounter = raw[3] & 0xf
	if p.AdaptationFieldControl == 0 {
		return nil, fmt.Errorf("ts: reserved adaptation_field_control at offset %d", off)
	}
	idx := 4
	if p.HasAdaptation() {
		afl := int(raw[idx])
		if idx+1+afl > PacketSize {
			return nil, fmt.Errorf("ts: adaptation field overruns packet at offset %d", off)
		}
		if afl >= 1 {
			flags := raw[idx+1]
			p.DiscontinuityIndicator = flags&0x80 != 0
			if flags&0x10 != 0 && afl >= 7 {
				b := raw[idx+2 : idx+8]
				p.PCRBase = uint64(b[0])<<25 | uint64(b[1])<<17 | uint64(b[2])<<9 | uint64(b[3])<<1 | uint64(b[4])>>7
				p.HasPCR = true
			}
		}
		idx += 1 + afl
	}
	if p.HasPayload() {
		if idx > PacketSize {
			return nil, fmt.Errorf("ts: corrupt adaptation/payload layout at offset %d", off)
		}
		p.Payload = p.Raw[idx:]
	}
	return p, nil
}
