package stream

// This file builds deterministic synthetic MP4 fixtures for tests and for
// the self-test CLI. Nothing external is read. Video samples carry an
// AVC-shaped payload whose first byte encodes the frame index, so a cutter
// can verify byte-exact sample selection without a decoder.

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/abema/go-mp4"
)

// Config describes a fixture.
type Config struct {
	Timescale   uint32
	FPS         int
	Width       uint16
	Height      uint16
	SampleCount int
	// Keyframe indices. When nil, every keyEvery-th sample is a keyframe.
	Keyframes []int
	KeyEvery  int
	// SampleSize overrides payload size; zero derives it from width/height.
	SampleSize int
	// TruncateBytes removes this many bytes from the end of the finished
	// file, simulating an interrupted write. Zero leaves the file intact.
	TruncateBytes int
}

// SamplePayload returns the deterministic payload for sample idx. The first
// two bytes are the big-endian index; a keyframe also carries a sync marker.
func SamplePayload(idx int, key bool, size int) []byte {
	if size < 4 {
		size = 4
	}
	p := make([]byte, size)
	binary.BigEndian.PutUint16(p[0:2], uint16(idx))
	if key {
		p[2] = 0x01
	} else {
		p[2] = 0x02
	}
	for i := 4; i < size; i++ {
		p[i] = byte((idx*31 + i) & 0xFF)
	}
	return p
}

// Build returns a complete progressive MP4 image.
func Build(cfg Config) ([]byte, error) {
	if cfg.Timescale == 0 {
		cfg.Timescale = 90000
	}
	if cfg.FPS <= 0 {
		cfg.FPS = 10
	}
	if cfg.SampleCount <= 0 {
		return nil, fmt.Errorf("stream: sample count must be positive")
	}
	if cfg.SampleSize <= 0 {
		cfg.SampleSize = 64
	}
	key := keySet(cfg)

	delta := cfg.Timescale / uint32(cfg.FPS)
	if delta == 0 {
		return nil, fmt.Errorf("stream: timescale %d too small for %d fps", cfg.Timescale, cfg.FPS)
	}

	var samples [][]byte
	var durations []uint32
	for i := 0; i < cfg.SampleCount; i++ {
		samples = append(samples, SamplePayload(i, key[i], cfg.SampleSize))
		durations = append(durations, delta)
	}

	// Lay mdat payload out with one chunk per sample so the chunk offset
	// table maps 1:1 and stays easy to verify.
	offsets := make([]uint32, len(samples))
	moovSize, err := buildMoovSize(cfg, durations, key, samples)
	if err != nil {
		return nil, err
	}

	// ftypSize is measured, not assumed: the writer emits one compatible
	// brand per entry plus the major brand and minor version.
	ftypSize := ftypLen(cfg)
	const mdatHeader = 8
	dataStart := ftypSize + moovSize + mdatHeader
	cur := dataStart
	for i, s := range samples {
		offsets[i] = uint32(cur)
		cur += len(s)
	}

	moov, err := buildMoov(cfg, durations, key, samples, offsets)
	if err != nil {
		return nil, err
	}
	if len(moov) != moovSize {
		return nil, fmt.Errorf("stream: moov size drift %d != %d", len(moov), moovSize)
	}

	out := &memWS{}
	if err := writeFtyp(out); err != nil {
		return nil, err
	}
	out.Write(moov)

	writeBox(out, mp4.BoxTypeMdat(), func(w *mp4.Writer) error {
		for _, s := range samples {
			if _, err := w.Write(s); err != nil {
				return err
			}
		}
		return nil
	})

	img := out.buf
	if cfg.TruncateBytes > 0 && cfg.TruncateBytes < len(img) {
		img = img[:len(img)-cfg.TruncateBytes]
	}
	return img, nil
}

// ftypBrands returns the brands written into ftyp.
func ftypBrands() [][4]byte {
	return [][4]byte{
		{'i', 's', 'o', 'm'},
		{'i', 's', 'o', '2'},
		{'a', 'v', 'c', '1'},
		{'m', 'p', '4', '1'},
	}
}

// ftypLen is the exact ftyp box length for those brands.
func ftypLen(Config) int { return 16 + 4*len(ftypBrands()) }

func writeFtyp(w io.WriteSeeker) error {
	var brands []mp4.CompatibleBrandElem
	for _, b := range ftypBrands() {
		brands = append(brands, mp4.CompatibleBrandElem{CompatibleBrand: b})
	}
	return writeBox(w, mp4.BoxTypeFtyp(), func(mw *mp4.Writer) error {
		_, err := mp4.Marshal(mw, &mp4.Ftyp{
			MajorBrand:       [4]byte{'i', 's', 'o', 'm'},
			MinorVersion:     512,
			CompatibleBrands: brands,
		}, mp4.Context{})
		return err
	})
}

func keySet(cfg Config) map[int]bool {
	key := make(map[int]bool)
	if len(cfg.Keyframes) > 0 {
		for _, k := range cfg.Keyframes {
			key[k] = true
		}
		return key
	}
	every := cfg.KeyEvery
	if every <= 0 {
		every = 10
	}
	for i := 0; i < cfg.SampleCount; i += every {
		key[i] = true
	}
	return key
}

func buildMoovSize(cfg Config, durations []uint32, key map[int]bool, samples [][]byte) (int, error) {
	m, err := buildMoov(cfg, durations, key, samples, make([]uint32, len(samples)))
	if err != nil {
		return 0, err
	}
	return len(m), nil
}

func buildMoov(cfg Config, durations []uint32, key map[int]bool, samples [][]byte, offsets []uint32) ([]byte, error) {
	ms := &memWS{}
	w := mp4.NewWriter(ms)
	var buildErr error
	stage := "?"
	wrap := func(fn func() error) {
		if buildErr == nil {
			if err := fn(); err != nil {
				buildErr = fmt.Errorf("stream: building %s: %w", stage, err)
			}
		}
	}
	setStage := func(s string) { stage = s }

	setStage("Moov")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); return err })

	// mvhd
	setStage("Mvhd")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); return err })
	wrap(func() error {
		_, err := mp4.Marshal(w, &mp4.Mvhd{
			Timescale:   cfg.Timescale,
			DurationV0:  uint32(len(samples)) * durations[0],
			Rate:        0x00010000,
			Volume:      0x0100,
			Matrix:      identityMatrix(),
			NextTrackID: 2,
		}, mp4.Context{})
		return err
	})
	wrap(func() error { _, err := w.EndBox(); return err })

	// trak
	setStage("Trak")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); return err })
	setStage("Tkhd")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTkhd()}); return err })
	tkhd := &mp4.Tkhd{
		TrackID:    1,
		DurationV0: uint32(len(samples)) * durations[0],
		Matrix:     identityMatrix(),
		Width:      uint32(cfg.Width) << 16,
		Height:     uint32(cfg.Height) << 16,
	}
	tkhd.SetFlags(0x000007) // enabled | in movie | in preview
	wrap(func() error {
		_, err := mp4.Marshal(w, tkhd, mp4.Context{})
		return err
	})
	wrap(func() error { _, err := w.EndBox(); return err })

	// mdia
	setStage("Mdia")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); return err })
	setStage("Mdhd")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); return err })
	wrap(func() error {
		_, err := mp4.Marshal(w, &mp4.Mdhd{
			Timescale:  cfg.Timescale,
			DurationV0: uint32(len(samples)) * durations[0],
			Language:   [3]byte{'u', 'n', 'd'},
		}, mp4.Context{})
		return err
	})
	wrap(func() error { _, err := w.EndBox(); return err })

	setStage("Hdlr")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}); return err })
	wrap(func() error {
		_, err := mp4.Marshal(w, &mp4.Hdlr{
			PreDefined:  0,
			HandlerType: [4]byte{'v', 'i', 'd', 'e'},
			Name:        "vide",
		}, mp4.Context{})
		return err
	})
	wrap(func() error { _, err := w.EndBox(); return err })

	// minf
	setStage("Minf")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); return err })
	setStage("Vmhd")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeVmhd()}); return err })
	wrap(func() error {
		vmhd := &mp4.Vmhd{}
		vmhd.SetFlags(1)
		_, err := mp4.Marshal(w, vmhd, mp4.Context{})
		return err
	})
	wrap(func() error { _, err := w.EndBox(); return err })

	// dinf/dref/url
	setStage("Dinf")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDinf()}); return err })
	setStage("Dref")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDref()}); return err })
	wrap(func() error {
		_, err := mp4.Marshal(w, &mp4.Dref{EntryCount: 1}, mp4.Context{})
		return err
	})
	setStage("Url")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeUrl()}); return err })
	wrap(func() error {
		url := &mp4.Url{}
		url.SetFlags(mp4.UrlSelfContained)
		_, err := mp4.Marshal(w, url, mp4.Context{})
		return err
	})
	wrap(func() error { _, err := w.EndBox(); return err })
	wrap(func() error { _, err := w.EndBox(); return err })
	wrap(func() error { _, err := w.EndBox(); return err })

	// stbl
	setStage("Stbl")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); return err })
	setStage("Stsd")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); return err })
	wrap(func() error {
		_, err := mp4.Marshal(w, &mp4.Stsd{FullBox: mp4.FullBox{Version: 0, Flags: [3]byte{0, 0, 0}}, EntryCount: 1}, mp4.Context{})
		return err
	})
	// avc1 sample entry with avcC inside
	setStage("Avc1")
	wrap(func() error {
		_, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeAvc1()})
		return err
	})
	avc1 := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeAvc1()},
			DataReferenceIndex: 1,
		},
		Width:           cfg.Width,
		Height:          cfg.Height,
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
		PreDefined3:     -1,
	}
	setStage("Avc1-entry")
	wrap(func() error {
		_, err := mp4.Marshal(w, avc1, mp4.Context{})
		return err
	})
	setStage("AvcC")
	setStage("AvcC")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeAvcC()}); return err })
	wrap(func() error {
		avcC := &mp4.AVCDecoderConfiguration{
			AnyTypeBox:                 mp4.AnyTypeBox{Type: mp4.BoxTypeAvcC()},
			ConfigurationVersion:       1,
			Profile:                    0x64,
			ProfileCompatibility:       0,
			Level:                      0x1f,
			LengthSizeMinusOne:         3,
			NumOfSequenceParameterSets: 1,
			SequenceParameterSets: []mp4.AVCParameterSet{
				{Length: 4, NALUnit: []byte{0x67, 0x64, 0x00, 0x1f}},
			},
			NumOfPictureParameterSets: 1,
			PictureParameterSets: []mp4.AVCParameterSet{
				{Length: 4, NALUnit: []byte{0x68, 0xEE, 0x3C, 0x80}},
			},
		}
		_, err := mp4.Marshal(w, avcC, mp4.Context{})
		return err
	})
	setStage("end-avcC")
	wrap(func() error { _, err := w.EndBox(); return err })
	setStage("end-avc1")
	wrap(func() error { _, err := w.EndBox(); return err })
	setStage("end-stsd")
	wrap(func() error { _, err := w.EndBox(); return err })

	// stts
	setStage("Stts")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStts()}); return err })
	wrap(func() error {
		_, err := mp4.Marshal(w, &mp4.Stts{
			EntryCount: 1,
			Entries:    []mp4.SttsEntry{{SampleCount: uint32(len(samples)), SampleDelta: durations[0]}},
		}, mp4.Context{})
		return err
	})
	wrap(func() error { _, err := w.EndBox(); return err })

	// stss with keyframe sample numbers (1-based)
	setStage("Stss")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStss()}); return err })
	var syncNums []uint32
	for i := range samples {
		if key[i] {
			syncNums = append(syncNums, uint32(i+1))
		}
	}
	wrap(func() error {
		_, err := mp4.Marshal(w, &mp4.Stss{
			EntryCount:   uint32(len(syncNums)),
			SampleNumber: syncNums,
		}, mp4.Context{})
		return err
	})
	wrap(func() error { _, err := w.EndBox(); return err })

	// stsc: one sample per chunk
	setStage("Stsc")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsc()}); return err })
	wrap(func() error {
		_, err := mp4.Marshal(w, &mp4.Stsc{
			EntryCount: 1,
			Entries:    []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1}},
		}, mp4.Context{})
		return err
	})
	wrap(func() error { _, err := w.EndBox(); return err })

	// stsz
	setStage("Stsz")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsz()}); return err })
	var sizes []uint32
	for _, s := range samples {
		sizes = append(sizes, uint32(len(s)))
	}
	wrap(func() error {
		_, err := mp4.Marshal(w, &mp4.Stsz{
			SampleCount: uint32(len(samples)),
			EntrySize:   sizes,
		}, mp4.Context{})
		return err
	})
	wrap(func() error { _, err := w.EndBox(); return err })

	// stco
	setStage("Stco")
	wrap(func() error { _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStco()}); return err })
	wrap(func() error {
		_, err := mp4.Marshal(w, &mp4.Stco{
			EntryCount:  uint32(len(offsets)),
			ChunkOffset: offsets,
		}, mp4.Context{})
		return err
	})
	wrap(func() error { _, err := w.EndBox(); return err })

	wrap(func() error { _, err := w.EndBox(); return err }) // stbl
	wrap(func() error { _, err := w.EndBox(); return err }) // minf
	wrap(func() error { _, err := w.EndBox(); return err }) // mdia
	wrap(func() error { _, err := w.EndBox(); return err }) // trak
	wrap(func() error { _, err := w.EndBox(); return err }) // moov

	if buildErr != nil {
		return nil, buildErr
	}
	return ms.buf, nil
}

func identityMatrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}

func writeBox(w io.WriteSeeker, typ mp4.BoxType, fill func(*mp4.Writer) error) error {
	mw := mp4.NewWriter(w)
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
		return err
	}
	if err := fill(mw); err != nil {
		return err
	}
	_, err := mw.EndBox()
	return err
}

// memWS is an in-memory io.WriteSeeker for building boxes. go-mp4 patches
// box sizes by seeking backwards, which bytes.Buffer alone cannot do.
type memWS struct {
	buf []byte
	pos int64
}

func (m *memWS) Write(p []byte) (int, error) {
	end := m.pos + int64(len(p))
	if end > int64(len(m.buf)) {
		grown := make([]byte, end)
		copy(grown, m.buf)
		m.buf = grown
	}
	copy(m.buf[m.pos:], p)
	m.pos = end
	return len(p), nil
}

func (m *memWS) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = m.pos + offset
	case io.SeekEnd:
		next = int64(len(m.buf)) + offset
	default:
		return 0, fmt.Errorf("stream: bad whence %d", whence)
	}
	if next < 0 {
		return 0, fmt.Errorf("stream: negative seek %d", next)
	}
	m.pos = next
	return next, nil
}
