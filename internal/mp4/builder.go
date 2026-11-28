package mp4

import (
	"bytes"
	"io"

	gomp4 "github.com/abema/go-mp4"
)

// SampleSpec describes one generated sample.
type SampleSpec struct {
	Payload []byte
	Delta   uint32
	IsKey   bool
}

// Builder emits a minimal single-video-track MP4 into memory. Every sample is
// placed in its own chunk so stco offsets map 1:1 to sample byte offsets.
type Builder struct {
	trackID    uint32
	timescale  uint32
	samples    []SampleSpec
	payloadBuf bytes.Buffer
	offsets    []uint64
}

// NewBuilder starts a video track with the given ID and timescale.
func NewBuilder(trackID, timescale uint32) *Builder {
	return &Builder{trackID: trackID, timescale: timescale}
}

// AddSample appends one sample and records its eventual mdat offset.
func (b *Builder) AddSample(s SampleSpec) {
	b.offsets = append(b.offsets, uint64(b.payloadBuf.Len()))
	b.payloadBuf.Write(s.Payload)
	b.samples = append(b.samples, s)
}

// marshal writes the whole moov tree via the go-mp4 writer.
func (b *Builder) writeMoov(w io.WriteSeeker) error {
	mp := gomp4.NewWriter(w)
	start := func(t gomp4.BoxType) { _, _ = mp.StartBox(&gomp4.BoxInfo{Type: t}) }
	end := func() { _, _ = mp.EndBox() }
	must := func(box gomp4.IImmutableBox) {
		if _, err := gomp4.Marshal(mp, box, gomp4.Context{}); err != nil {
			panic(err)
		}
	}

	start(gomp4.BoxTypeMoov())
	start(gomp4.BoxTypeTrak())
	start(gomp4.BoxTypeTkhd())
	must(&gomp4.Tkhd{TrackID: b.trackID, DurationV0: b.totalDuration()})
	end()
	start(gomp4.BoxTypeMdia())
	start(gomp4.BoxTypeMdhd())
	must(&gomp4.Mdhd{Timescale: b.timescale, DurationV0: b.totalDuration()})
	end()
	start(gomp4.BoxTypeHdlr())
	must(&gomp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "vide"})
	end()
	start(gomp4.BoxTypeMinf())
	start(gomp4.BoxTypeVmhd())
	must(&gomp4.Vmhd{})
	end()
	start(gomp4.BoxTypeDinf())
	start(gomp4.BoxTypeDref())
	must(&gomp4.Dref{EntryCount: 1})
	start(gomp4.BoxTypeUrl())
	must(&gomp4.Url{FullBox: gomp4.FullBox{Flags: [3]byte{0, 0, gomp4.UrlSelfContained}}})
	end() // url
	end() // dref
	end() // dinf
	start(gomp4.BoxTypeStbl())
	b.writeStbl(mp, start, end, must)
	end() // stbl
	end() // minf
	end() // mdia
	end() // trak
	end() // moov
	return nil
}

func (b *Builder) writeStbl(mp *gomp4.Writer, start func(gomp4.BoxType), end func(), must func(gomp4.IImmutableBox)) {
	start(gomp4.BoxTypeStts())
	var entries []gomp4.SttsEntry
	for _, s := range b.samples {
		entries = append(entries, gomp4.SttsEntry{SampleCount: 1, SampleDelta: s.Delta})
	}
	must(&gomp4.Stts{EntryCount: uint32(len(entries)), Entries: entries})
	end()

	var keys []uint32
	for i, s := range b.samples {
		if s.IsKey {
			keys = append(keys, uint32(i+1)) // stss is 1-based
		}
	}
	if len(keys) > 0 {
		start(gomp4.BoxTypeStss())
		must(&gomp4.Stss{EntryCount: uint32(len(keys)), SampleNumber: keys})
		end()
	}

	start(gomp4.BoxTypeStsc())
	must(&gomp4.Stsc{
		EntryCount: 1,
		Entries:    []gomp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1}},
	})
	end()

	start(gomp4.BoxTypeStsz())
	sizes := make([]uint32, len(b.samples))
	for i, s := range b.samples {
		sizes[i] = uint32(len(s.Payload))
	}
	must(&gomp4.Stsz{SampleCount: uint32(len(sizes)), EntrySize: sizes})
	end()

	start(gomp4.BoxTypeStco())
	// Offsets are patched after the full layout is known; emit placeholders.
	must(&gomp4.Stco{EntryCount: uint32(len(b.offsets)), ChunkOffset: make([]uint32, len(b.offsets))})
	end()
}

func (b *Builder) totalDuration() uint32 {
	var d uint32
	for _, s := range b.samples {
		d += s.Delta
	}
	return d
}

// Build produces the final MP4 bytes, patching stco offsets to absolute mdat
// payload positions.
func (b *Builder) Build() ([]byte, error) {
	var moov seekBuffer
	if err := b.writeMoov(&moov); err != nil {
		return nil, err
	}
	var out seekBuffer
	// ftyp
	ftyp := &gomp4.Ftyp{
		MajorBrand:       [4]byte{'i', 's', 'o', 'm'},
		MinorVersion:     0,
		CompatibleBrands: []gomp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}},
	}
	var ftypBox seekBuffer
	w := gomp4.NewWriter(&ftypBox)
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeFtyp()}); err != nil {
		return nil, err
	}
	if _, err := gomp4.Marshal(w, ftyp, gomp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	out.Write(ftypBox.Bytes())
	out.Write(moov.Bytes())

	mdatStart := int64(out.Len())
	mw := gomp4.NewWriter(&out)
	if _, err := mw.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMdat()}); err != nil {
		return nil, err
	}
	payloadStart := int64(out.Len())
	out.Write(b.payloadBuf.Bytes())
	if _, err := mw.EndBox(); err != nil {
		return nil, err
	}

	// Patch the stco placeholder entries: locate them by re-scanning.
	data := out.Bytes()
	if err := patchStco(data, b.offsets, payloadStart); err != nil {
		return nil, err
	}
	_ = mdatStart
	return data, nil
}
