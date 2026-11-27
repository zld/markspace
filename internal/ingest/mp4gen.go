package ingest

import (
	"fmt"

	mp4 "github.com/abema/go-mp4"
)

// CorruptKind selects a fixture corruption for negative tests.
type CorruptKind int

const (
	CorruptNone     CorruptKind = iota // well-formed file
	CorruptTruncate                    // file ends inside mdat
	CorruptBadIndex                    // stco points beyond the file
)

// MP4Spec describes a synthetic AAC-in-MP4 fixture.
type MP4Spec struct {
	Timescale   uint32 // mdhd timescale (ticks per second)
	SampleRate  uint32 // mp4a sample rate
	Channels    uint16
	Samples     int    // number of access units
	SampleBytes uint32 // fixed bytes per access unit
	Delta       uint32 // stts sample delta (ticks per access unit)
	Seed        uint32
	Corrupt     CorruptKind
}

// GenMP4 builds the fixture entirely in memory; no external assets.
func GenMP4(spec MP4Spec) ([]byte, error) {
	if spec.Samples <= 0 || spec.SampleBytes == 0 || spec.Timescale == 0 {
		return nil, fmt.Errorf("ingest: invalid MP4Spec %+v", spec)
	}
	ftypLen := int64(8 + 8 + 4) // header + major/minor + one compat brand
	mdatLen := int64(spec.Samples) * int64(spec.SampleBytes)
	chunkOff := uint32(ftypLen + 8) // mdat payload follows ftyp + mdat header
	if spec.Corrupt == CorruptBadIndex {
		chunkOff = uint32(ftypLen+16+mdatLen) + 4096 // beyond end of file
	}
	sb := &seekBuf{}
	w := mp4.NewWriter(sb)
	ctx := mp4.Context{}
	box := func(t mp4.BoxType, b mp4.IImmutableBox) error {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: t}); err != nil {
			return err
		}
		if b != nil {
			if _, err := mp4.Marshal(w, b, ctx); err != nil {
				return err
			}
		}
		_, err := w.EndBox()
		return err
	}
	start := func(t mp4.BoxType) error {
		_, err := w.StartBox(&mp4.BoxInfo{Type: t})
		return err
	}
	end := func() error { _, err := w.EndBox(); return err }

	stts := &mp4.Stts{EntryCount: 1, Entries: []mp4.SttsEntry{
		{SampleCount: uint32(spec.Samples), SampleDelta: spec.Delta}}}
	sizes := make([]uint32, spec.Samples)
	for i := range sizes {
		sizes[i] = spec.SampleBytes
	}
	dur := uint32(spec.Samples) * spec.Delta
	steps := []func() error{
		func() error {
			return box(mp4.BoxTypeFtyp(), &mp4.Ftyp{
				MajorBrand:       [4]byte{'i', 's', 'o', 'm'},
				CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}},
			})
		},
		func() error { return start(mp4.StrToBoxType("mdat")) },
		func() error { _, err := w.Write(genPayload(spec.Seed, 0, int(mdatLen))); return err },
		end,
		func() error { return start(mp4.BoxTypeMoov()) },
		func() error {
			return box(mp4.BoxTypeMvhd(), &mp4.Mvhd{
				Timescale: spec.Timescale, DurationV0: dur,
				Rate: 0x10000, Volume: 0x0100, NextTrackID: 1,
				Matrix: [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
			})
		},
		func() error { return start(mp4.BoxTypeTrak()) },
		func() error { return start(mp4.BoxTypeMdia()) },
		func() error {
			return box(mp4.BoxTypeMdhd(), &mp4.Mdhd{Timescale: spec.Timescale, DurationV0: dur})
		},
		func() error {
			return box(mp4.BoxTypeHdlr(), &mp4.Hdlr{HandlerType: [4]byte{'s', 'o', 'u', 'n'}, Name: "SoundHandler"})
		},
		func() error { return start(mp4.BoxTypeMinf()) },
		func() error { return box(mp4.BoxTypeSmhd(), &mp4.Smhd{}) },
		func() error { return start(mp4.BoxTypeStbl()) },
		func() error { return start(mp4.BoxTypeStsd()) },
		func() error { _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, ctx); return err },
		func() error {
			return box(mp4.BoxTypeMp4a(), &mp4.AudioSampleEntry{
				SampleEntry: mp4.SampleEntry{
					AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeMp4a()},
					DataReferenceIndex: 1,
				},
				ChannelCount: spec.Channels, SampleSize: 16,
				SampleRate: spec.SampleRate << 16,
			})
		},
		end, // stsd
		func() error { return box(mp4.BoxTypeStts(), stts) },
		func() error {
			return box(mp4.BoxTypeStsc(), &mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{
				{FirstChunk: 1, SamplesPerChunk: uint32(spec.Samples), SampleDescriptionIndex: 1}}})
		},
		func() error {
			return box(mp4.BoxTypeStsz(), &mp4.Stsz{SampleCount: uint32(spec.Samples), EntrySize: sizes})
		},
		func() error {
			return box(mp4.BoxTypeStco(), &mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{chunkOff}})
		},
		end, end, end, end, end, // stbl minf mdia trak moov
	}
	for _, s := range steps {
		if err := s(); err != nil {
			return nil, err
		}
	}
	out := sb.buf
	if spec.Corrupt == CorruptTruncate {
		cut := ftypLen + 8 + mdatLen/2 // end the file inside mdat
		out = out[:cut]
	}
	return out, nil
}

// seekBuf is an in-memory io.WriteSeeker for mp4.Writer.
type seekBuf struct {
	buf []byte
	pos int64
}

func (s *seekBuf) Write(p []byte) (int, error) {
	end := s.pos + int64(len(p))
	if grow := end - int64(len(s.buf)); grow > 0 {
		s.buf = append(s.buf, make([]byte, grow)...)
	}
	copy(s.buf[s.pos:], p)
	s.pos = end
	return len(p), nil
}

func (s *seekBuf) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case 0:
		s.pos = off
	case 1:
		s.pos += off
	case 2:
		s.pos = int64(len(s.buf)) + off
	}
	if s.pos < 0 {
		return 0, fmt.Errorf("ingest: negative seek")
	}
	return s.pos, nil
}
