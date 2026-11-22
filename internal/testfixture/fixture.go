// Package testfixture generates the MP4, RTP and manifest samples used by the
// demo command and the test suite. Every sample is produced in-process from a
// seed: no external media files are referenced, and identical seeds reproduce
// byte-identical fixtures.
package testfixture

import (
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"

	mp4 "github.com/abema/go-mp4"
)

// MP4Spec configures a generated MP4 container.
type MP4Spec struct {
	Seed          int64
	Timescale     uint32 // media timescale (e.g. 48000)
	SampleCount   int
	SampleDelta   uint32 // stts delta in timescale units
	SampleSize    uint32 // constant per-sample payload size
	KeyframeEvery int    // stss entry every N samples; 0 disables stss
	StartOffset   uint32 // non-zero first-sample media time (timescale units)
}

// seekBuffer is an in-memory io.WriteSeeker for the go-mp4 writer.
type seekBuffer struct {
	buf []byte
	pos int64
}

func (s *seekBuffer) Write(p []byte) (int, error) {
	end := s.pos + int64(len(p))
	if end > int64(len(s.buf)) {
		s.buf = append(s.buf, make([]byte, end-int64(len(s.buf)))...)
	}
	copy(s.buf[s.pos:end], p)
	s.pos = end
	return len(p), nil
}

func (s *seekBuffer) Seek(offset int64, whence int) (int64, error) {
	var p int64
	switch whence {
	case io.SeekStart:
		p = offset
	case io.SeekCurrent:
		p = s.pos + offset
	case io.SeekEnd:
		p = int64(len(s.buf)) + offset
	}
	if p < 0 {
		return 0, fmt.Errorf("negative seek offset")
	}
	s.pos = p
	return p, nil
}

func (s *seekBuffer) Bytes() []byte { return s.buf }

// GenerateMP4 builds a single-audio-track MP4 matching spec. Payload bytes are
// deterministic functions of the seed so the digest is stable across runs.
func GenerateMP4(spec MP4Spec) ([]byte, error) {
	if spec.Timescale == 0 || spec.SampleCount <= 0 || spec.SampleDelta == 0 {
		return nil, fmt.Errorf("invalid MP4Spec: %+v", spec)
	}
	rng := rand.New(rand.NewSource(spec.Seed))
	payload := make([]byte, spec.SampleCount*int(spec.SampleSize))
	if _, err := rng.Read(payload); err != nil {
		return nil, err
	}

	sb := &seekBuffer{}
	w := mp4.NewWriter(sb)
	boxStart := func(t mp4.BoxType) {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: t}); err != nil {
			panic(err)
		}
	}
	boxEnd := func() {
		if _, err := w.EndBox(); err != nil {
			panic(err)
		}
	}
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}

	// ftyp
	boxStart(mp4.BoxTypeFtyp())
	_, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand:       [4]byte{'i', 's', 'o', 'm'},
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}},
	}, mp4.Context{})
	must(err)
	boxEnd()

	duration := uint64(spec.SampleCount)*uint64(spec.SampleDelta) + uint64(spec.StartOffset)
	// moov
	boxStart(mp4.BoxTypeMoov())
	boxStart(mp4.BoxTypeMvhd())
	_, err = mp4.Marshal(w, &mp4.Mvhd{Timescale: 1000, DurationV0: uint32(duration * 1000 / uint64(spec.Timescale))}, mp4.Context{})
	must(err)
	boxEnd()

	// trak
	boxStart(mp4.BoxTypeTrak())
	boxStart(mp4.BoxTypeTkhd())
	_, err = mp4.Marshal(w, &mp4.Tkhd{TrackID: 1,
		DurationV0: uint32(duration * 1000 / uint64(spec.Timescale)), Volume: 0x0100}, mp4.Context{})
	must(err)
	boxEnd()

	// mdia
	boxStart(mp4.BoxTypeMdia())
	boxStart(mp4.BoxTypeMdhd())
	_, err = mp4.Marshal(w, &mp4.Mdhd{Timescale: spec.Timescale, DurationV0: uint32(duration)}, mp4.Context{})
	must(err)
	boxEnd()
	boxStart(mp4.BoxTypeHdlr())
	_, err = mp4.Marshal(w, &mp4.Hdlr{HandlerType: [4]byte{'s', 'o', 'u', 'n'}}, mp4.Context{})
	must(err)
	boxEnd()

	// minf
	boxStart(mp4.BoxTypeMinf())
	boxStart(mp4.BoxTypeSmhd())
	_, err = mp4.Marshal(w, &mp4.Smhd{}, mp4.Context{})
	must(err)
	boxEnd()
	boxStart(mp4.BoxTypeDinf())
	boxStart(mp4.BoxTypeDref())
	_, err = mp4.Marshal(w, &mp4.Dref{EntryCount: 1}, mp4.Context{})
	must(err)
	boxEnd()
	boxEnd()

	// stbl
	boxStart(mp4.BoxTypeStbl())
	boxStart(mp4.BoxTypeStsd())
	_, err = mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{})
	must(err)
	// Nested mp4a audio sample entry so Probe classifies the track as audio.
	boxStart(mp4.BoxTypeMp4a())
	_, err = mp4.Marshal(w, &mp4.AudioSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeMp4a()},
			DataReferenceIndex: 1,
		},
		ChannelCount: 1,
		SampleSize:   16,
		SampleRate:   spec.Timescale << 16, // fixed-point 16.16
	}, mp4.Context{})
	must(err)
	boxEnd()
	boxEnd()
	// totalSamples accounts for the leading offset sample used to model a
	// non-zero first-sample media timestamp.
	offsetSamples := 0
	if spec.StartOffset > 0 {
		offsetSamples = 1
	}
	totalSamples := spec.SampleCount + offsetSamples

	boxStart(mp4.BoxTypeStts())
	sttsEntries := []mp4.SttsEntry{{SampleCount: uint32(spec.SampleCount), SampleDelta: spec.SampleDelta}}
	if spec.StartOffset > 0 {
		sttsEntries = []mp4.SttsEntry{
			{SampleCount: 1, SampleDelta: spec.StartOffset},
			{SampleCount: uint32(spec.SampleCount), SampleDelta: spec.SampleDelta},
		}
	}
	_, err = mp4.Marshal(w, &mp4.Stts{EntryCount: uint32(len(sttsEntries)), Entries: sttsEntries}, mp4.Context{})
	must(err)
	boxEnd()
	if spec.KeyframeEvery > 0 {
		var keys []uint32
		// stss is 1-based; real samples follow the leading offset sample.
		for i := 1; i <= spec.SampleCount; i += spec.KeyframeEvery {
			keys = append(keys, uint32(i+offsetSamples))
		}
		boxStart(mp4.BoxTypeStss())
		_, err = mp4.Marshal(w, &mp4.Stss{EntryCount: uint32(len(keys)), SampleNumber: keys}, mp4.Context{})
		must(err)
		boxEnd()
	}
	boxStart(mp4.BoxTypeStsc())
	_, err = mp4.Marshal(w, &mp4.Stsc{EntryCount: 1,
		Entries: []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: uint32(totalSamples)}}}, mp4.Context{})
	must(err)
	boxEnd()
	// The leading offset sample carries no payload (size 0); real samples follow.
	sizes := make([]uint32, totalSamples)
	for i := offsetSamples; i < totalSamples; i++ {
		sizes[i] = spec.SampleSize
	}
	boxStart(mp4.BoxTypeStsz())
	_, err = mp4.Marshal(w, &mp4.Stsz{SampleCount: uint32(totalSamples), EntrySize: sizes}, mp4.Context{})
	must(err)
	boxEnd()
	// stco placeholder offset 0; patched after mdat is written.
	boxStart(mp4.BoxTypeStco())
	stcoPos := sb.pos
	_, err = mp4.Marshal(w, &mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{0}}, mp4.Context{})
	must(err)
	boxEnd()
	boxEnd() // stbl
	boxEnd() // minf
	boxEnd() // mdia
	boxEnd() // trak
	boxEnd() // moov

	// mdat
	boxStart(mp4.BoxTypeMdat())
	mdatPayloadPos := sb.pos
	_, err = w.Write(payload)
	must(err)
	boxEnd()

	// Patch the single chunk offset to point at the mdat payload.
	if _, err := sb.Seek(stcoPos+12, io.SeekStart); err != nil { // after box header(8)+version/flags(4)
		return nil, err
	}
	if err := binary.Write(sb, binary.BigEndian, uint32(mdatPayloadPos)); err != nil {
		return nil, err
	}
	if _, err := sb.Seek(0, io.SeekEnd); err != nil {
		return nil, err
	}
	return sb.Bytes(), nil
}

// CorruptMP4 damages an MP4 byte slice in-place style by truncating the tail
// (simulating an interrupted write). The moov is placed before mdat so the
// index remains parseable while the payload is incomplete.
func CorruptMP4(data []byte) []byte {
	if len(data) < 64 {
		return data
	}
	return append([]byte(nil), data[:len(data)*3/4]...)
}
