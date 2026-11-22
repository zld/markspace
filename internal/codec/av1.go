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

// AV1Report summarises a parsed OBU stream.
type AV1Report struct {
	Units        int
	Keyframes    int
	Truncated    int
	HeaderDamage int
	FirstDamage  int64
	Bytes        int64
}

// OBU is one AV1 Open Bitstream Unit located by absolute offset.
type OBU struct {
	Offset    int64 // first byte of the OBU header
	HeaderLen int64 // header byte plus leb128 size field
	Payload   int64 // declared payload length
	Type      uint8
	Keyframe  bool
	Truncated bool // declared payload extends past the stream end
	HasSize   bool
}

// End reports the declared end of the OBU.
func (o OBU) End() int64 { return o.Offset + o.HeaderLen + o.Payload }

// AV1Parser incrementally walks OBUs using their leb128 size fields. It keeps
// only the bytes of the OBU currently under construction.
type AV1Parser struct {
	buf       []byte
	bufBase   int64
	units     int
	keyframes int
	truncated int
	hdrDamage int
	firstBad  int64
	bytes     int64
	out       []OBU
}

// NewAV1Parser builds an incremental AV1 OBU parser.
func NewAV1Parser() *AV1Parser { return &AV1Parser{firstBad: -1} }

// Feed appends one chunk and emits every OBU fully contained in the buffer.
func (p *AV1Parser) Feed(data []byte) []OBU {
	if len(data) == 0 {
		return nil
	}
	p.bytes += int64(len(data))
	p.buf = append(p.buf, data...)
	for len(p.buf) >= 2 {
		obu, need := p.inspect(p.buf)
		if need > 0 {
			break
		}
		if obu == nil {
			// Unparseable byte: report and resynchronize by dropping it.
			p.hdrDamage++
			p.noteDamage(p.bufBase)
			p.buf = p.buf[1:]
			p.bufBase++
			continue
		}
		if obu.Truncated {
			break
		}
		n := int(obu.End() - p.bufBase)
		if n > len(p.buf) {
			break
		}
		p.emit(*obu)
		p.buf = p.buf[n:]
		p.bufBase += int64(n)
	}
	return p.takeOut()
}

// inspect decodes the header at the front of buf. need>0 means more bytes are
// required before the OBU is decidable; nil with need==0 means the header is
// invalid.
func (p *AV1Parser) inspect(b []byte) (*OBU, int) {
	hdr := b[0]
	if hdr&0x80 != 0 {
		return nil, 0
	}
	typ := (hdr >> 3) & 0x0F
	ext := hdr&0x04 != 0
	hasSize := hdr&0x02 != 0
	if !hasSize {
		return nil, 0
	}
	pos := 1
	if ext {
		if pos >= len(b) {
			return nil, 1
		}
		pos++
	}
	size := uint64(0)
	shift := uint(0)
	for {
		if pos >= len(b) {
			return nil, 1
		}
		c := b[pos]
		pos++
		size |= uint64(c&0x7F) << shift
		if c&0x80 == 0 {
			break
		}
		shift += 7
		if shift > 56 {
			return nil, 0
		}
	}
	o := &OBU{Offset: p.bufBase, HeaderLen: int64(pos), Payload: int64(size), Type: typ, HasSize: true}
	o.Keyframe = typ == OBUFrame || typ == OBUFrameHeader || typ == OBUSequenceHeader
	o.Truncated = o.End() > p.bufBase+int64(len(b))
	return o, 0
}

func (p *AV1Parser) emit(o OBU) {
	p.units++
	if o.Keyframe {
		p.keyframes++
	}
	p.out = append(p.out, o)
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

// Flush reports a final OBU whose declared size exceeds the available bytes as
// an unrecoverable truncation, and returns it.
func (p *AV1Parser) Flush() []OBU {
	if len(p.buf) == 0 {
		return nil
	}
	obu, need := p.inspect(p.buf)
	if need > 0 || obu == nil {
		p.truncated++
		p.noteDamage(p.bufBase)
		o := OBU{Offset: p.bufBase, HeaderLen: int64(len(p.buf)), Type: 0xFF, Truncated: true}
		p.units++
		p.out = append(p.out, o)
		p.buf = nil
		return p.takeOut()
	}
	if obu.Truncated || obu.End() > p.bufBase+int64(len(p.buf)) {
		obu.Truncated = true
		p.truncated++
		p.noteDamage(obu.Offset)
		p.units++
		if obu.Keyframe {
			p.keyframes++
		}
		p.out = append(p.out, *obu)
	} else {
		p.emit(*obu)
	}
	p.buf = nil
	return p.takeOut()
}

// Counts returns cumulative OBU and keyframe totals.
func (p *AV1Parser) Counts() (units, keyframes int) { return p.units, p.keyframes }

// Report returns the cumulative parse report.
func (p *AV1Parser) Report() AV1Report {
	bad := int64(-1)
	if p.truncated > 0 || p.hdrDamage > 0 {
		bad = p.firstBad
	}
	return AV1Report{Units: p.units, Keyframes: p.keyframes, Truncated: p.truncated, HeaderDamage: p.hdrDamage, FirstDamage: bad, Bytes: p.bytes}
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
	hdr := byte(typ<<3) | 0x02 // has_size_field=1
	out := []byte{hdr}
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
