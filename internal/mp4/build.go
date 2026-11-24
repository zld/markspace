package mp4

import (
	"encoding/binary"
	"fmt"
)

// SampleSpec is one self-generated media sample.
type SampleSpec struct {
	Data     []byte
	Duration uint32
}

// SegmentSpec describes one fragment.
type SegmentSpec struct {
	Sequence      uint32
	StartTicks    int64
	DurationTicks uint32
	Timescale     uint32
	Samples       []SampleSpec
	EMSG          *EMSGEvent
	IncludeSidx   *bool
}

// RepresentationSpec is a complete generated presentation layer.
type RepresentationSpec struct {
	ID          string
	Timescale   uint32
	Segments    []SegmentSpec
	IncludeSidx bool
}

func be16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

func be32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func be64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func box(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func fullBox(version byte, flags uint32, payload []byte) []byte {
	head := uint32(version)<<24 | (flags & 0x00ffffff)
	return append(be32(head), payload...)
}

// BuildInit builds a minimal ftyp+moov initial segment with mdhd timescale.
func BuildInit(timescale uint32) []byte {
	ftypPayload := append([]byte("isom"), be32(0x200)...)
	ftypPayload = append(ftypPayload, []byte("isomiso6dash")...)
	ftyp := box("ftyp", ftypPayload)

	mdhdPayload := fullBox(0, 0, nil)
	mdhdPayload = append(mdhdPayload, be32(0)...) // creation
	mdhdPayload = append(mdhdPayload, be32(0)...) // modification
	mdhdPayload = append(mdhdPayload, be32(timescale)...)
	mdhdPayload = append(mdhdPayload, be32(0)...) // duration unknown
	mdhdPayload = append(mdhdPayload, be16(0x55c4)...)
	mdhdPayload = append(mdhdPayload, be16(0)...)
	mdhd := box("mdhd", mdhdPayload)
	mdia := box("mdia", mdhd)
	trak := box("trak", mdia)
	moov := box("moov", trak)
	return append(ftyp, moov...)
}

// BuildRepresentation builds init and all configured segments.
func BuildRepresentation(spec RepresentationSpec) ([]byte, error) {
	if spec.Timescale == 0 {
		return nil, fmt.Errorf("mp4 build: timescale required")
	}
	out := BuildInit(spec.Timescale)
	for i := range spec.Segments {
		seg := spec.Segments[i]
		if seg.Timescale == 0 {
			seg.Timescale = spec.Timescale
		}
		includeSidx := spec.IncludeSidx
		if seg.IncludeSidx != nil {
			includeSidx = *seg.IncludeSidx
		}
		seg.IncludeSidx = &includeSidx
		b, err := BuildSegment(seg)
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	return out, nil
}

// BuildSegment builds sidx/emsg/moof/mdat for one fragment.
func BuildSegment(seg SegmentSpec) ([]byte, error) {
	if seg.Timescale == 0 {
		return nil, fmt.Errorf("mp4 build: segment timescale required")
	}
	samples := seg.Samples
	if len(samples) == 0 {
		samples = []SampleSpec{{Data: []byte(fmt.Sprintf("segment-%d", seg.Sequence)), Duration: 1000}}
	}

	trunPayload := fullBox(0, 0x000300, nil) // sample duration + sample size
	trunPayload = append(trunPayload, be32(uint32(len(samples)))...)
	var mdatPayload []byte
	var duration uint32
	for i, sample := range samples {
		data := sample.Data
		if data == nil {
			data = []byte(fmt.Sprintf("seq-%d-sample-%d", seg.Sequence, i))
		}
		d := sample.Duration
		if d == 0 {
			d = seg.DurationTicks / uint32(len(samples))
		}
		duration += d
		trunPayload = append(trunPayload, be32(d)...)
		trunPayload = append(trunPayload, be32(uint32(len(data)))...)
		mdatPayload = append(mdatPayload, data...)
	}
	if seg.DurationTicks == 0 {
		seg.DurationTicks = duration
	}

	mfhd := box("mfhd", fullBox(0, 0, be32(seg.Sequence)))
	tfhd := box("tfhd", fullBox(0, 0, be32(1)))
	tfdt := box("tfdt", fullBox(1, 0, be64(uint64(seg.StartTicks))))
	trun := box("trun", trunPayload)
	traf := box("traf", append(append(tfhd, tfdt...), trun...))
	moof := box("moof", append(mfhd, traf...))
	mdat := box("mdat", mdatPayload)

	var out []byte
	if seg.IncludeSidx == nil || *seg.IncludeSidx {
		out = append(out, buildSidx(seg, uint32(len(moof)+len(mdat)))...)
	}
	if seg.EMSG != nil {
		out = append(out, BuildEMSG(*seg.EMSG)...)
	}
	out = append(out, moof...)
	out = append(out, mdat...)
	return out, nil
}

func buildSidx(seg SegmentSpec, referenceSize uint32) []byte {
	p := fullBox(0, 0, nil)
	p = append(p, be32(1)...)                        // reference ID
	p = append(p, be32(seg.Timescale)...)            // timescale
	p = append(p, be32(uint32(seg.StartTicks))...)   // earliest PTS, fixtures fit v0
	p = append(p, be32(0)...)                        // first offset
	p = append(p, be16(0)...)                        // reserved
	p = append(p, be16(1)...)                        // reference count
	p = append(p, be32(referenceSize&0x7fffffff)...) // reference size
	p = append(p, be32(seg.DurationTicks)...)        // segment duration
	p = append(p, be32(0x80000000)...)               // starts with SAP
	return box("sidx", p)
}

// BuildEMSG encodes an emsg version 1 box.
func BuildEMSG(e EMSGEvent) []byte {
	if e.Timescale == 0 {
		e.Timescale = 1000
	}
	if e.SchemeIDURI == "" {
		e.SchemeIDURI = "urn:avpipe:timed"
	}
	p := fullBox(1, 0, nil)
	p = append(p, []byte(e.SchemeIDURI)...)
	p = append(p, 0)
	p = append(p, []byte(e.Value)...)
	p = append(p, 0)
	p = append(p, be32(e.Timescale)...)
	p = append(p, be64(e.PTS)...)
	p = append(p, be32(e.Duration)...)
	p = append(p, be32(e.ID)...)
	p = append(p, e.Message...)
	return box("emsg", p)
}
