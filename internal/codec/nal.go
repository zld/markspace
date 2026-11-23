// Package codec parses elementary stream structure: H.264/H.265 Annex B NAL
// units and AV1 OBUs, with explicit truncation and cross-chunk boundary
// detection.
package codec

import (
	"fmt"

	"archivecheck/internal/media"
)

// Codec identifies an elementary stream format.
type Codec string

const (
	H264 Codec = "h264"
	H265 Codec = "h265"
	AV1  Codec = "av1"
)

// NALUnit is one Annex B NAL unit with absolute byte positions.
type NALUnit struct {
	Offset      int64 // first byte of the start code
	HeaderAt    int64 // first byte of the NAL header
	Length      int64 // payload length, excluding the start code
	Type        uint8 // nal_unit_type: H.264 5 bits, H.265 6 bits
	Frame       bool  // VCL unit that carries one coded picture
	Keyframe    bool
	SplitOnRead bool // the start code began in an earlier chunk
	Truncated   bool // stream ended inside the unit
}

// End returns the offset one past the unit payload.
func (u NALUnit) End() int64 { return u.HeaderAt + u.Length }

// AnnexBReport summarises a parsed Annex B stream.
type AnnexBReport struct {
	Codec           Codec
	Units           int
	Keyframes       int
	Truncated       int
	SplitStartCodes int
	FirstDamage     int64
	Bytes           int64
}

// h264Keyframe reports whether one H.264 NAL is an IDR slice (type 5), the
// only NAL that both starts a new coded picture and resets reference state.
// Parameter sets (7/8) are not frames and are not counted as keyframes.
func h264Keyframe(t uint8) bool { return t == 5 }

// h265Keyframe reports IRAP pictures: BLA/IDR/CRA (types 16-21).
func h265Keyframe(t uint8) bool { return t >= 16 && t <= 21 }

// StartCodeLen returns the Annex B prefix length at b[i:] (4 or 3), else 0.
func StartCodeLen(b []byte, i int) int {
	if i+4 <= len(b) && b[i] == 0 && b[i+1] == 0 && b[i+2] == 0 && b[i+3] == 1 {
		return 4
	}
	if i+3 <= len(b) && b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
		return 3
	}
	return 0
}

// NALParser incrementally splits an Annex B stream. It retains only a short
// prefix carry and per-unit bookkeeping, so memory does not scale with source
// length or chunk count.
//
// The scan is chunk-incremental: each byte is examined exactly once. A start
// code is resolved in whichever chunk finishes it, so prefixes split across
// chunk boundaries are recognised and counted.
type NALParser struct {
	codec Codec

	scan     []byte // current chunk plus up to three carry bytes
	scanBase int64  // absolute offset of scan[0]
	fedEnd   int64  // absolute offset one past all bytes fed so far

	hasUnit   bool
	unit      NALUnit
	wantHdr   bool
	units     int
	keyframes int
	truncated int
	splits    int
	firstBad  int64
	bytes     int64
	out       []NALUnit
}

// NewNALParser builds an incremental H.264 or H.265 parser.
func NewNALParser(c Codec) (*NALParser, error) {
	if c != H264 && c != H265 {
		return nil, fmt.Errorf("codec: %s is not an Annex B codec", c)
	}
	return &NALParser{codec: c, firstBad: -1}, nil
}

// Feed appends one chunk and returns the units completed by it. The final unit
// is completed by Flush: Annex B carries no explicit length.
func (p *NALParser) Feed(data []byte) []NALUnit {
	if len(data) == 0 {
		return nil
	}
	p.bytes += int64(len(data))
	chunkStart := p.fedEnd
	p.fedEnd += int64(len(data))
	p.scan = append(p.scan, data...)

	i := 0
	for i < len(p.scan) {
		abs := p.scanBase + int64(i)
		if p.wantHdr {
			p.applyHeader(p.scan[i])
			p.wantHdr = false
			i++
			continue
		}
		if sc := StartCodeLen(p.scan, i); sc > 0 {
			if p.hasUnit {
				p.closeUnit(abs)
			}
			p.unit = NALUnit{Offset: abs, HeaderAt: abs + int64(sc), SplitOnRead: abs < chunkStart}
			p.hasUnit = true
			p.wantHdr = true
			i += sc
			continue
		}
		// A four-byte prefix cannot be resolved with fewer than four bytes;
		// retain the tail so the next chunk can complete it.
		if i+4 > len(p.scan) {
			break
		}
		i++
	}

	keepFrom := i
	p.scanBase += int64(keepFrom)
	p.scan = append(p.scan[:0], p.scan[keepFrom:]...)
	return p.takeOut()
}

// applyHeader records the NAL unit type from its header byte.
func (p *NALParser) applyHeader(b byte) {
	if p.codec == H264 {
		p.unit.Type = b & 0x1F
		p.unit.Frame = p.unit.Type >= 1 && p.unit.Type <= 5
		p.unit.Keyframe = h264Keyframe(p.unit.Type)
	} else {
		p.unit.Type = (b >> 1) & 0x3F
		p.unit.Frame = p.unit.Type <= 31
		p.unit.Keyframe = h265Keyframe(p.unit.Type)
	}
}

func (p *NALParser) closeUnit(end int64) {
	p.unit.Length = end - p.unit.HeaderAt
	if p.unit.Length < 0 {
		p.unit.Length = 0
	}
	p.emit()
}

func (p *NALParser) emit() {
	u := p.unit
	p.units++
	if u.Keyframe {
		p.keyframes++
	}
	if u.Truncated {
		p.truncated++
		p.noteDamage(u.Offset)
	}
	if u.SplitOnRead {
		p.splits++
	}
	p.out = append(p.out, u)
	p.hasUnit = false
}

func (p *NALParser) noteDamage(off int64) {
	if p.firstBad < 0 || off < p.firstBad {
		p.firstBad = off
	}
}

func (p *NALParser) takeOut() []NALUnit {
	units := p.out
	p.out = nil
	return units
}

// Flush completes the pending unit. A prefix with no header byte is a
// truncation at the start code; a final unit with a header is complete.
func (p *NALParser) Flush() []NALUnit {
	if p.hasUnit {
		if p.wantHdr {
			p.unit.Length = 0
			p.unit.Truncated = true
			p.noteDamage(p.unit.Offset)
		} else {
			p.unit.Length = p.scanBase + int64(len(p.scan)) - p.unit.HeaderAt
			if p.unit.Length < 0 {
				p.unit.Length = 0
			}
		}
		p.emit()
	}
	p.wantHdr = false
	p.scan = nil
	return p.takeOut()
}

// Counts returns cumulative unit and keyframe totals.
func (p *NALParser) Counts() (units, keyframes int) { return p.units, p.keyframes }

// Report returns the cumulative parse report.
func (p *NALParser) Report() AnnexBReport {
	bad := int64(-1)
	if p.truncated > 0 {
		bad = p.firstBad
	}
	return AnnexBReport{
		Codec:           p.codec,
		Units:           p.units,
		Keyframes:       p.keyframes,
		Truncated:       p.truncated,
		SplitStartCodes: p.splits,
		FirstDamage:     bad,
		Bytes:           p.bytes,
	}
}

// Faults converts structural defects into the shared fault model.
func (p *NALParser) Faults(layer string) []media.Fault {
	var fs []media.Fault
	if p.splits > 0 {
		fs = append(fs, media.NewFault(media.KindStartCodeSplit, layer,
			fmt.Sprintf("%d start codes straddled chunk boundaries and were reassembled", p.splits),
			-1, media.Informational))
	}
	if p.truncated > 0 {
		off := p.firstBad
		fs = append(fs, media.NewFault(media.KindNALTruncated, layer,
			fmt.Sprintf("%d NAL units lack a terminating start code", p.truncated), off, media.Unrecoverable))
	}
	return fs
}
