package packageindex

import (
	"bytes"
	"encoding/binary"
	"io"

	"example.com/mediavault/internal/fault"
)

// ArchiveSpec describes a deterministic MP4 wrapper written around one payload.
// The wrapper is a normal ISO-BMFF audio/metadata file so that the same scanner
// validates our own archives and foreign fixtures alike.
type ArchiveSpec struct {
	SampleRate    uint32
	Channels      uint16
	BitsPerSample uint16
	SampleCount   uint32
	Timescale     uint32
	Duration      uint64
	Handler       string // "soun" for PCM, "meta" for a timeline
	PayloadSize   int64
	// ChunkOffsets are byte offsets relative to the start of the mdat payload.
	ChunkOffsets []int64
	// ChunkFrames gives the frame count of each chunk for stsc runs. It may be
	// nil for metadata archives.
	ChunkFrames []int64
	// ConstantSampleSize is the stsz sample size; zero means the table is
	// omitted and the payload is treated as one sample.
	ConstantSampleSize uint32
}

const (
	ftypSize    = 24
	mdatHdrSize = 8
	// WriteArchive lays out exactly one ftyp, then the mdat header, then the
	// payload, then the moov. buildMoov therefore knows where every chunk is.
	payloadStart = ftypSize + mdatHdrSize
)

// WriteArchive streams ftyp + mdat + moov to w without ever buffering the
// payload. It returns the total byte count written.
func WriteArchive(w io.Writer, spec ArchiveSpec, payload io.Reader) (int64, error) {
	if spec.PayloadSize < 0 || spec.PayloadSize > 1<<31 {
		return 0, fault.New(fault.CodeLimit, fault.StageArchived, -1, "archive payload %d exceeds 2 GiB", spec.PayloadSize)
	}
	if spec.Handler == "" {
		spec.Handler = "soun"
	}
	if spec.Timescale == 0 {
		spec.Timescale = spec.SampleRate
	}
	if spec.Timescale == 0 {
		spec.Timescale = 1000
	}
	moov, err := buildMoov(spec)
	if err != nil {
		return 0, err
	}
	head := make([]byte, 0, ftypSize+mdatHdrSize)
	head = append(head, box("ftyp", []byte("iso6"), u32(0), []byte("iso6mp41"))...)
	head = append(head, box("mdat", nil)...)
	// The mdat size field must include the payload; patch it in place.
	binary.BigEndian.PutUint32(head[ftypSize:ftypSize+4], uint32(mdatHdrSize+spec.PayloadSize))
	var written int64
	n, err := w.Write(head)
	written += int64(n)
	if err != nil {
		return written, fault.Wrap(fault.CodeIO, fault.StageArchived, written, err, "write archive header")
	}
	if spec.PayloadSize > 0 {
		if payload == nil {
			return written, fault.New(fault.CodeIO, fault.StageArchived, written, "payload is missing")
		}
		m, err := io.CopyN(w, payload, spec.PayloadSize)
		written += m
		if err != nil {
			return written, fault.Wrap(fault.CodeIO, fault.StageArchived, written, err, "write archive payload")
		}
	}
	n, err = w.Write(moov)
	written += int64(n)
	if err != nil {
		return written, fault.Wrap(fault.CodeIO, fault.StageArchived, written, err, "write moov")
	}
	return written, nil
}

func buildMoov(spec ArchiveSpec) ([]byte, error) {
	stco := make([]byte, 0, 8+4*len(spec.ChunkOffsets))
	stco = append(stco, u32(uint32(len(spec.ChunkOffsets)))...)
	for _, off := range spec.ChunkOffsets {
		abs := payloadStart + off
		if abs < 0 || abs > int64(^uint32(0)) {
			return nil, fault.New(fault.CodeMP4Extent, fault.StageArchived, off, "chunk offset %d exceeds 32-bit stco", abs)
		}
		stco = append(stco, u32(uint32(abs))...)
	}

	var stsz []byte
	if spec.ConstantSampleSize > 0 {
		stsz = append(u32(spec.ConstantSampleSize), u32(spec.SampleCount)...)
	} else {
		stsz = append(u32(0), u32(1)...)
		stsz = append(stsz, u32(uint32(spec.PayloadSize))...)
	}

	var stsc []byte
	runs := chunkRuns(spec.ChunkFrames)
	stsc = append(stsc, u32(uint32(len(runs)))...)
	for i, r := range runs {
		stsc = append(stsc, u32(uint32(i+1))...)
		stsc = append(stsc, u32(uint32(r))...)
		stsc = append(stsc, u32(1)...)
	}

	stts := append(u32(1), u32(spec.SampleCount)...)
	stts = append(stts, u32(1)...)

	stbl := box("stbl",
		fullbox("stsd", 0, 0, append(u32(1), sampleEntry(spec, 0)...)),
		fullbox("stts", 0, 0, stts),
		fullbox("stsc", 0, 0, stsc),
		fullbox("stsz", 0, 0, stsz),
		fullbox("stco", 0, 0, stco),
	)
	minf := box("minf",
		fullbox("smhd", 0, 0, []byte{0, 0, 0, 0}),
		box("dinf", fullbox("dref", 0, 0, append(u32(1), fullbox("url ", 0, 1, nil)...))),
		stbl,
	)
	mdhdPayload := append(u32(0), u32(0)...)
	mdhdPayload = append(mdhdPayload, u32(spec.Timescale)...)
	mdhdPayload = append(mdhdPayload, u32(uint32(spec.Duration))...)
	mdhdPayload = append(mdhdPayload, 0x55, 0xC4, 0, 0)
	mdia := box("mdia",
		fullbox("mdhd", 0, 0, mdhdPayload),
		fullbox("hdlr", 0, 0, hdlrPayload(spec.Handler)),
		minf,
	)
	tkhdPayload := make([]byte, 0, 84)
	tkhdPayload = append(tkhdPayload, u32(0)...) // creation
	tkhdPayload = append(tkhdPayload, u32(0)...) // modification
	tkhdPayload = append(tkhdPayload, u32(1)...) // track id
	tkhdPayload = append(tkhdPayload, u32(0)...) // reserved
	tkhdPayload = append(tkhdPayload, u32(uint32(spec.Duration))...)
	tkhdPayload = append(tkhdPayload, make([]byte, 8)...) // reserved
	tkhdPayload = append(tkhdPayload, 0, 0)               // layer
	tkhdPayload = append(tkhdPayload, 0, 0)               // alternate group
	if spec.Handler == "soun" {
		tkhdPayload = append(tkhdPayload, 0x01, 0x00)
	} else {
		tkhdPayload = append(tkhdPayload, 0, 0)
	}
	tkhdPayload = append(tkhdPayload, 0, 0) // reserved
	tkhdPayload = append(tkhdPayload, matrix()...)
	tkhdPayload = append(tkhdPayload, u32(0)...) // width
	tkhdPayload = append(tkhdPayload, u32(0)...) // height
	trak := box("trak",
		fullbox("tkhd", 0, 7, tkhdPayload),
		mdia,
	)
	mvhdPayload := make([]byte, 0, 100)
	mvhdPayload = append(mvhdPayload, u32(0)...) // creation
	mvhdPayload = append(mvhdPayload, u32(0)...) // modification
	mvhdPayload = append(mvhdPayload, u32(spec.Timescale)...)
	mvhdPayload = append(mvhdPayload, u32(uint32(spec.Duration))...)
	mvhdPayload = append(mvhdPayload, u32(0x00010000)...) // rate 1.0
	mvhdPayload = append(mvhdPayload, 0x01, 0x00)         // volume 1.0
	mvhdPayload = append(mvhdPayload, 0, 0)
	mvhdPayload = append(mvhdPayload, make([]byte, 8)...)
	mvhdPayload = append(mvhdPayload, matrix()...)
	mvhdPayload = append(mvhdPayload, make([]byte, 24)...)
	mvhdPayload = append(mvhdPayload, u32(2)...)
	moovPayload := append(fullbox("mvhd", 0, 0, mvhdPayload), trak...)
	return box("moov", moovPayload), nil
}

func hdlrPayload(handler string) []byte {
	name := "SoundHandler\x00"
	if handler != "soun" {
		name = "TimelineHandler\x00"
	}
	p := make([]byte, 0, 24+len(name))
	p = append(p, u32(0)...)
	p = append(p, []byte(handler)...)
	p = append(p, make([]byte, 12)...)
	p = append(p, []byte(name)...)
	return p
}

func sampleEntry(spec ArchiveSpec, dref uint16) []byte {
	typ := "sowt"
	if spec.Handler != "soun" {
		typ = "text"
	}
	p := make([]byte, 0, 28)
	p = append(p, make([]byte, 6)...)
	p = append(p, byte(dref>>8), byte(dref))
	p = append(p, make([]byte, 8)...) // version, revision, vendor
	p = append(p, byte(spec.Channels>>8), byte(spec.Channels))
	p = append(p, byte(spec.BitsPerSample>>8), byte(spec.BitsPerSample))
	p = append(p, make([]byte, 4)...)
	rate := spec.SampleRate
	if rate > 0xFFFF {
		rate = 0
	}
	p = append(p, byte(rate>>8), byte(rate), 0, 0)
	return box(typ, p)
}

func chunkRuns(frames []int64) []int64 {
	var runs []int64
	for _, f := range frames {
		if len(runs) == 0 || runs[len(runs)-1] != f {
			runs = append(runs, f)
		}
	}
	if len(runs) == 0 {
		runs = []int64{1}
	}
	return runs
}

func matrix() []byte {
	return []byte{
		0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0x40, 0, 0, 0, 0, 0, 0, 0,
	}
}

func box(typ string, payload ...[]byte) []byte {
	var body bytes.Buffer
	for _, p := range payload {
		body.Write(p)
	}
	out := make([]byte, 8+body.Len())
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], body.Bytes())
	return out
}

func fullbox(typ string, version byte, flags uint32, payload []byte) []byte {
	hdr := make([]byte, 4)
	hdr[0] = version
	hdr[1] = byte(flags >> 16)
	hdr[2] = byte(flags >> 8)
	hdr[3] = byte(flags)
	return box(typ, append(hdr, payload...))
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}
