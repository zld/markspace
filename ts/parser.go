// Package ts parses self-generated MPEG-TS streams: 188/204-byte packets,
// PIDs, adaptation fields, PCR/PTS, continuity counters and PSI boundaries.
package ts

import (
	"io"

	"medkernel/media"
)

const (
	SyncByte     byte   = 0x47
	Size188             = 188
	Size204             = 204
	headerLen           = 4
	PIDPAT       uint16 = 0x0000
	adaptPCR     uint8  = 0x10
	adaptDiscont uint8  = 0x80
)

// Packet is one parsed TS packet. Slices alias the parser's read buffer and
// remain valid only until the next Parse call; copy what you need to retain.
type Packet struct {
	Index         int64
	Offset        int64 // byte offset of the sync byte in the source
	Size          int
	TEI           bool
	PUSI          bool
	PID           uint16
	Scrambling    uint8
	HasAdaptation bool
	HasPayload    bool
	CC            uint8
	AdaptFlags    uint8
	Adaptation    []byte
	Payload       []byte
	PCR27         int64  // raw 27 MHz PCR when present, else -1
	PTS90         uint64 // raw 33-bit PES PTS when PUSI PES present, else 0
	HasPTS        bool
	Raw           []byte
}

// Discontinuity returns the adaptation-field discontinuity indicator.
func (p Packet) Discontinuity() bool { return p.HasAdaptation && p.AdaptFlags&adaptDiscont != 0 }

// Stats summarizes a structural parse pass.
type Stats struct {
	Total      int64
	SyncErrors int64
	Truncated  int64
	TEI        int64
	PCRPresent int64
	PTSPresent int64
	BytesRead  int64
	PacketSize int
}

// Parser performs a bounded-memory, packet-at-a-time structural parse.
type Parser struct {
	Stats  Stats
	Faults []*media.MediaError
	size   int // 0 = auto-detect on first packet
	buf    []byte
}

// NewParser builds a parser. size is 188, 204 or 0 for auto-detection.
func NewParser(size int) *Parser {
	if size != 0 && size != Size188 && size != Size204 {
		size = 0
	}
	return &Parser{size: size, buf: make([]byte, Size204)}
}

// Parse reads every packet from r, invoking visit for each well-formed one.
// Structural faults are collected in p.Faults; a fatal read error (anything
// other than a clean EOF / trailing partial packet) is returned directly.
func (p *Parser) Parse(r io.Reader, visit func(Packet) error) error {
	br := &byteReader{r: r}
	for {
		sync, ok := br.next()
		if !ok {
			return br.err
		}
		off := br.read - 1
		if sync != SyncByte {
			p.Stats.SyncErrors++
			p.Faults = append(p.Faults, media.New(media.ClassSync, "ts.parse",
				"missing 0x47 sync byte").WithIndex(p.Stats.Total).
				WithRange(off, off+1))
			// Resynchronize: discard bytes up to the next sync byte.
			for {
				b, ok := br.next()
				if !ok {
					return br.err
				}
				if b == SyncByte {
					br.unread()
					break
				}
			}
			continue
		}
		size, consumed, ok := p.resolveSize(br)
		if !ok {
			p.Stats.Truncated++
			p.Faults = append(p.Faults, media.New(media.ClassTruncated, "ts.parse",
				"packet truncated before declared length").
				WithIndex(p.Stats.Total).WithRange(off, off+int64(p.effectiveSize())))
			return nil
		}
		full := p.buf[:size]
		full[0] = SyncByte
		// resolveSize already buffered the first `consumed` bytes after sync;
		// only read the remainder (the 16 FEC bytes of a 204-byte packet).
		if !br.readN(full[consumed+1:]) {
			p.Stats.Truncated++
			p.Faults = append(p.Faults, media.New(media.ClassTruncated, "ts.parse",
				"packet truncated before declared length").
				WithIndex(p.Stats.Total).WithRange(off, off+int64(size)))
			return nil
		}
		p.Stats.BytesRead = br.read
		pkt, perr := p.decode(full, off, size)
		if perr != nil {
			p.Faults = append(p.Faults, perr)
		}
		p.Stats.Total++
		if pkt.TEI {
			p.Stats.TEI++
		}
		if pkt.PCR27 >= 0 {
			p.Stats.PCRPresent++
		}
		if pkt.HasPTS {
			p.Stats.PTSPresent++
		}
		if visit != nil {
			if err := visit(pkt); err != nil {
				return err
			}
		}
	}
}

func (p *Parser) effectiveSize() int {
	if p.size != 0 {
		return p.size
	}
	return Size188
}

// resolveSize fixes the packet size once. With auto-detection it peeks the
// byte at the 188-stride: a sync byte there means 204-byte packets (16 FEC
// trailing bytes), otherwise standard 188-byte packets. It returns the number
// of post-sync bytes already buffered into p.buf.
func (p *Parser) resolveSize(br *byteReader) (size, consumed int, ok bool) {
	if p.size != 0 {
		return p.size, 0, true
	}
	peek := make([]byte, Size188) // bytes at offsets 1..188
	if !br.readN(peek) {
		return 0, 0, false
	}
	size = Size188
	if peek[Size188-1] == SyncByte {
		size = Size204
	}
	p.size = size
	p.Stats.PacketSize = size
	copy(p.buf[1:], peek)
	return size, Size188, true
}

func (p *Parser) decode(b []byte, off int64, size int) (Packet, *media.MediaError) {
	var ferr *media.MediaError
	h := uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	pkt := Packet{
		Index:         p.Stats.Total,
		Offset:        off,
		Size:          size,
		TEI:           b[1]&0x80 != 0,
		PUSI:          b[1]&0x40 != 0,
		PID:           uint16(h&0x1FFF00) >> 8,
		Scrambling:    b[3] >> 6,
		HasAdaptation: b[3]&0x20 != 0,
		HasPayload:    b[3]&0x10 != 0,
		CC:            b[3] & 0x0F,
		PCR27:         -1,
		Raw:           b,
	}
	if pkt.TEI {
		ferr = media.New(media.ClassSync, "ts.parse",
			"transport error indicator set").WithPID(pkt.PID).
			WithIndex(pkt.Index).WithRange(off, off+int64(size))
	}
	pos := headerLen
	if pkt.HasAdaptation {
		if pos >= len(b) {
			return pkt, media.New(media.ClassTruncated, "ts.parse",
				"adaptation field missing").WithPID(pkt.PID).
				WithIndex(pkt.Index).WithRange(off, off+int64(size))
		}
		al := int(b[pos])
		pos++
		if al == 0 {
			pkt.HasAdaptation = false // length 0 is legal but means no field
		} else {
			end := pos + al
			if end > Size188 {
				return pkt, media.New(media.ClassTruncated, "ts.parse",
					"adaptation field exceeds packet").WithPID(pkt.PID).
					WithIndex(pkt.Index).WithRange(off, off+int64(size))
			}
			pkt.AdaptFlags = b[pos]
			pkt.Adaptation = b[pos:end]
			if pkt.AdaptFlags&adaptPCR != 0 && al >= 7 {
				pkt.PCR27 = decodePCR(b[pos+1:])
			}
			pos = end
		}
	}
	if pkt.HasPayload && pos < Size188 {
		pkt.Payload = b[pos:Size188]
		if pkt.PUSI {
			if pts, ok := parsePESPTS(pkt.Payload); ok {
				pkt.HasPTS, pkt.PTS90 = true, pts
			}
		}
	}
	return pkt, ferr
}

// decodePCR reads the 6-byte adaptation-field PCR and returns 27 MHz units.
func decodePCR(b []byte) int64 {
	base := int64(b[0])<<25 | int64(b[1])<<17 | int64(b[2])<<9 |
		int64(b[3])<<1 | int64(b[4])>>7
	ext := int64(b[4]&0x01)<<8 | int64(b[5])
	return base*300 + ext
}

// parsePESPTS extracts the 33-bit PTS from a PES header at payload start.
func parsePESPTS(payload []byte) (uint64, bool) {
	if len(payload) < 14 || payload[0] != 0x00 || payload[1] != 0x00 || payload[2] != 0x01 {
		return 0, false
	}
	if payload[7]&0x80 == 0 { // PTS_DTS_flags top bit
		return 0, false
	}
	b := payload[9:]
	pts := uint64(b[0]&0x0E)<<29 | uint64(b[1])<<22 |
		uint64(b[2]&0xFE)<<14 | uint64(b[3])<<7 | uint64(b[4])>>1
	return pts, true
}

// byteReader is a single-byte-pushback reader with total byte accounting.
type byteReader struct {
	r     io.Reader
	tail  byte
	push  bool
	read  int64
	err   error
}

func (b *byteReader) next() (byte, bool) {
	if b.push {
		b.push = false
		return b.tail, true
	}
	var buf [1]byte
	n, err := io.ReadFull(b.r, buf[:])
	b.read += int64(n)
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			b.err = nil
			return 0, false
		}
		b.err = media.Wrap(media.ClassIO, "ts.parse", err, "stream read failed")
		return 0, false
	}
	b.tail = buf[0]
	return buf[0], true
}

func (b *byteReader) unread() { b.push = true; b.read-- }

func (b *byteReader) readN(dst []byte) bool {
	n, err := io.ReadFull(b.r, dst)
	b.read += int64(n)
	return err == nil
}
