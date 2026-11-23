package codec

import (
	"fmt"

	"archivecheck/internal/media"
)

// AV1 OBU type codes (AV1 spec §5.3.1).
const (
	OBUSequenceHeader = 1
	OBUTemporalDelim  = 2
	OBUFrameHeader    = 3
	OBUTileGroup      = 4
	OBUMetadata       = 5
	OBUFrame          = 6
	OBURedundantFrame = 7
	OBUPadding        = 15
)

// OBU is one AV1 Open Bitstream Unit located by absolute offset.
type OBU struct {
	Offset    int64 // first byte of the OBU header
	HeaderLen int64 // header byte, optional extension, and leb128 size field
	Payload   int64 // declared payload length
	Type      uint8
	Frame     bool // carries coded frame data or its frame header
	Keyframe  bool // a complete frame OBU (type 6)
	Truncated bool // declared payload extends past end of stream
}

// End reports the declared end of the OBU.
func (o OBU) End() int64 { return o.Offset + o.HeaderLen + o.Payload }

// AV1Report summarises a parsed OBU stream.
type AV1Report struct {
	Units        int
	Keyframes    int
	Frames       int
	Truncated    int
	HeaderDamage int
	FirstDamage  int64
	Bytes        int64
}

// AV1Parser walks OBUs using their leb128 size fields. State is a fixed-size
// header accumulator plus a skip counter: payload bytes are counted, never
// buffered, so memory does not scale with the source or OBU size.
type AV1Parser struct {
	pos   int64 // absolute offset of the next unread byte
	bytes int64

	hdr      [10]byte // pending header bytes
	hdrLen   int
	hdrStart int64
	inHeader bool

	skip      int64 // payload bytes still to skip
	cur       OBU
	seenFrame bool

	units     int
	keyframes int
	frames    int
	truncated int
	hdrDamage int
	firstBad  int64
	out       []OBU
}

// NewAV1Parser builds an incremental AV1 OBU parser.
func NewAV1Parser() *AV1Parser { return &AV1Parser{firstBad: -1} }

// Feed appends one chunk and emits every OBU whose header and payload both
// completed within the stream so far.
func (p *AV1Parser) Feed(data []byte) []OBU {
	p.bytes += int64(len(data))
	i := 0
	for i < len(data) {
		if p.skip > 0 {
			n := int64(len(data) - i)
			if n > p.skip {
				n = p.skip
			}
			p.skip -= n
			p.pos += n
			i += int(n)
			if p.skip == 0 {
				p.complete(p.cur)
			}
			continue
		}
		if !p.inHeader {
			p.inHeader = true
			p.hdrLen = 0
			p.hdrStart = p.pos
		}
		p.hdr[p.hdrLen] = data[i]
		p.hdrLen++
		i++
		p.pos++
		obu, need := p.tryHeader()
		if need {
			continue
		}
		p.inHeader = false
		if obu == nil {
			// Invalid header byte: report and resynchronize past it.
			p.hdrDamage++
			p.noteDamage(p.hdrStart)
			// Re-scan bytes already accumulated after the bad first byte.
			rest := append([]byte(nil), p.hdr[1:p.hdrLen]...)
			p.hdrLen = 0
			p.pos = p.hdrStart + 1
			if len(rest) > 0 {
				p.Feed(rest)
			}
			return p.takeOut()
		}
		p.cur = *obu
		p.skip = obu.Payload
		if p.skip == 0 {
			p.complete(p.cur)
		}
	}
	return p.takeOut()
}

// tryHeader decodes the pending header. need=true means more bytes are
// required; nil with need=false means the header is invalid.
func (p *AV1Parser) tryHeader() (*OBU, bool) {
	b := p.hdr[:p.hdrLen]
	hdr := b[0]
	if hdr&0x80 != 0 {
		return nil, false
	}
	if hdr&0x02 == 0 {
		// Without a size field OBUs are only delimited by temporal units,
		// which this archive format does not use.
		return nil, false
	}
	typ := (hdr >> 3) & 0x0F
	pos := 1
	if hdr&0x04 != 0 {
		if pos >= len(b) {
			return nil, true
		}
		pos++
	}
	size := uint64(0)
	shift := uint(0)
	for {
		if pos >= len(b) {
			return nil, true
		}
		c := b[pos]
		pos++
		size |= uint64(c&0x7F) << shift
		if c&0x80 == 0 {
			break
		}
		shift += 7
		if shift > 56 {
			return nil, false
		}
	}
	o := &OBU{Offset: p.hdrStart, HeaderLen: int64(pos), Payload: int64(size), Type: typ}
	o.Frame = typ == OBUFrameHeader || typ == OBUTileGroup || typ == OBUFrame || typ == OBURedundantFrame
	o.Keyframe = typ == OBUFrame
	return o, false
}

func (p *AV1Parser) complete(o OBU) {
	p.units++
	if o.Frame {
		p.frames++
	}
	if o.Keyframe {
		p.keyframes++
	}
	p.out = append(p.out, o)
	p.skip = 0
}

func (p *AV1Parser) noteDamage(off int64) {
	if p.firstBad < 0 || off < p.firstBad {
		p.firstBad = off
	}
}

func (p *AV1Parser) takeOut() []OBU {
	out := p.out
	p.out = nil
	return out
}

// Flush reports a final OBU whose declared payload exceeds the bytes received.
// The OBU is returned with Truncated set so callers can prove the loss.
func (p *AV1Parser) Flush() []OBU {
	if p.skip > 0 {
		o := p.cur
		o.Truncated = true
		p.truncated++
		p.noteDamage(o.Offset)
		p.units++
		if o.Frame {
			p.frames++
		}
		if o.Keyframe {
			p.keyframes++
		}
		p.out = append(p.out, o)
		p.skip = 0
	} else if p.inHeader && p.hdrLen > 0 {
		// A dangling header with no payload is a truncated header.
		o := OBU{Offset: p.hdrStart, HeaderLen: int64(p.hdrLen), Type: 0xFF, Truncated: true}
		p.truncated++
		p.noteDamage(o.Offset)
		p.units++
		p.out = append(p.out, o)
		p.inHeader = false
	}
	return p.takeOut()
}

// Counts returns cumulative OBU, frame and keyframe totals.
func (p *AV1Parser) Counts() (units, frames, keyframes int) { return p.units, p.frames, p.keyframes }

// Report returns the cumulative parse report.
func (p *AV1Parser) Report() AV1Report {
	bad := int64(-1)
	if p.truncated > 0 || p.hdrDamage > 0 {
		bad = p.firstBad
	}
	return AV1Report{Units: p.units, Keyframes: p.keyframes, Frames: p.frames,
		Truncated: p.truncated, HeaderDamage: p.hdrDamage, FirstDamage: bad, Bytes: p.bytes}
}

// Faults converts structural defects into the shared fault model.
func (p *AV1Parser) Faults(layer string) []media.Fault {
	var fs []media.Fault
	if p.truncated > 0 {
		fs = append(fs, media.NewFault(media.KindOBUTruncated, layer,
			fmt.Sprintf("%d OBUs declare more payload than the stream provides", p.truncated),
			p.firstBad, media.Unrecoverable))
	}
	if p.hdrDamage > 0 {
		fs = append(fs, media.NewFault(media.KindNALTruncated, layer,
			fmt.Sprintf("%d bytes could not be decoded as an OBU header", p.hdrDamage),
			p.firstBad, media.Recoverable))
	}
	return fs
}

// EncodeOBU builds one OBU with a leb128 size field. It is used by the sample
// generator and tests.
func EncodeOBU(typ uint8, payload []byte) []byte {
	out := []byte{typ<<3 | 0x02} // has_size_field=1
	n := uint64(len(payload))
	for {
		b := byte(n & 0x7F)
		n >>= 7
		if n != 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			break
		}
	}
	return append(out, payload...)
}
