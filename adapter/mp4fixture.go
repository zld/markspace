package adapter

import (
	"encoding/binary"
	"fmt"
	"io"

	mp4 "github.com/abema/go-mp4"
)

// MP4Spec describes a generated single-track MP4 fixture.
type MP4Spec struct {
	Timescale uint32
	Delta     uint32 // ticks between samples
	Sizes     []uint32
	Keyframes []uint32 // 1-based sample numbers
	Emsg      [][]byte // raw emsg payloads (unknown metadata preservation)
}

// WriteMP4 serializes the spec as a minimal valid ISOBMFF file.
func WriteMP4(w io.WriteSeeker, spec MP4Spec) error {
	if spec.Timescale == 0 || spec.Delta == 0 || len(spec.Sizes) == 0 {
		return fmt.Errorf("mp4 fixture: invalid spec")
	}
	ww := mp4.NewWriter(w)
	// ftyp
	if _, err := ww.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(ww, &mp4.Ftyp{MajorBrand: [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0x200, CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}}}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := ww.EndBox(); err != nil {
		return err
	}
	for _, emsg := range spec.Emsg {
		if err := writeRawBox(ww, mp4.BoxTypeEmsg(), emsg); err != nil {
			return err
		}
	}
	// mdat with one chunk, sizes matching stsz/stco exactly.
	mdatBI, err := ww.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()})
	if err != nil {
		return err
	}
	payloadStart := mdatBI.Offset + mdatBI.HeaderSize
	mdatOffsets := make([]uint32, len(spec.Sizes))
	var cursor uint32
	for i, size := range spec.Sizes {
		mdatOffsets[i] = uint32(payloadStart) + cursor
		buf := make([]byte, size)
		binary.BigEndian.PutUint32(buf, sampleMagic)
		if _, err := ww.Write(buf); err != nil {
			return err
		}
		cursor += size
	}
	if _, err := ww.EndBox(); err != nil {
		return err
	}
	if err := writeMoov(ww, spec, mdatOffsets); err != nil {
		return err
	}
	return nil
}

func writeRawBox(ww *mp4.Writer, typ mp4.BoxType, payload []byte) error {
	if _, err := ww.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
		return err
	}
	if _, err := ww.Write(payload); err != nil {
		return err
	}
	_, err := ww.EndBox()
	return err
}

func writeMoov(ww *mp4.Writer, spec MP4Spec, absOffsets []uint32) error {
	n := uint32(len(spec.Sizes))
	if _, err := ww.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	if err := marshalBox(ww, mp4.BoxTypeMvhd(), &mp4.Mvhd{Timescale: spec.Timescale,
		DurationV0: spec.Delta * n, Rate: 0x10000, NextTrackID: 2,
		Matrix: [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}}); err != nil {
		return err
	}
	if _, err := ww.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if err := marshalBox(ww, mp4.BoxTypeTkhd(), &mp4.Tkhd{TrackID: 1, DurationV0: spec.Delta * n,
		Matrix: [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
		Width:  320 << 16,
		Height: 180 << 16}); err != nil {
		return err
	}
	if _, err := ww.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if err := marshalBox(ww, mp4.BoxTypeMdhd(), &mp4.Mdhd{Timescale: spec.Timescale,
		DurationV0: spec.Delta * n, Language: [3]byte{'u', 'n', 'd'}}); err != nil {
		return err
	}
	if err := marshalBox(ww, mp4.BoxTypeHdlr(), &mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'},
		Name: "video"}); err != nil {
		return err
	}
	if _, err := ww.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if err := marshalBox(ww, mp4.BoxTypeVmhd(), &mp4.Vmhd{}); err != nil {
		return err
	}
	if _, err := ww.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDinf()}); err != nil {
		return err
	}
	if err := marshalBox(ww, mp4.BoxTypeDref(), &mp4.Dref{EntryCount: 1}); err != nil {
		return err
	}
	if _, err := ww.EndBox(); err != nil { // dinf
		return err
	}
	if _, err := ww.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if err := marshalBox(ww, mp4.BoxTypeStts(), &mp4.Stts{EntryCount: 1,
		Entries: []mp4.SttsEntry{{SampleCount: n, SampleDelta: spec.Delta}}}); err != nil {
		return err
	}
	if err := marshalBox(ww, mp4.BoxTypeStsc(), &mp4.Stsc{EntryCount: 1,
		Entries: []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1}}}); err != nil {
		return err
	}
	if err := marshalBox(ww, mp4.BoxTypeStsz(), &mp4.Stsz{SampleSize: 0, SampleCount: n,
		EntrySize: append([]uint32(nil), spec.Sizes...)}); err != nil {
		return err
	}
	// stco stores absolute file offsets captured when mdat was written.
	if err := marshalBox(ww, mp4.BoxTypeStco(), &mp4.Stco{EntryCount: n, ChunkOffset: absOffsets}); err != nil {
		return err
	}
	if len(spec.Keyframes) > 0 {
		if err := marshalBox(ww, mp4.BoxTypeStss(), &mp4.Stss{EntryCount: uint32(len(spec.Keyframes)),
			SampleNumber: append([]uint32(nil), spec.Keyframes...)}); err != nil {
			return err
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := ww.EndBox(); err != nil { // stbl, minf, mdia
			return err
		}
	}
	if _, err := ww.EndBox(); err != nil { // trak
		return err
	}
	if _, err := ww.EndBox(); err != nil { // moov
		return err
	}
	return nil
}

func marshalBox(ww *mp4.Writer, typ mp4.BoxType, box mp4.IImmutableBox) error {
	if _, err := ww.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(ww, box, mp4.Context{}); err != nil {
		return err
	}
	_, err := ww.EndBox()
	return err
}

// seekableBuffer is an in-memory io.WriteSeeker. Seek only moves the cursor;
// subsequent writes overwrite bytes in place (go-mp4 rewrites box headers via
// seek-back) and grow the slice when writing past the current end.
type seekableBuffer struct {
	b   []byte
	pos int
}

func (s *seekableBuffer) Write(p []byte) (int, error) {
	end := s.pos + len(p)
	if end > len(s.b) {
		s.b = append(s.b, make([]byte, end-len(s.b))...)
	}
	copy(s.b[s.pos:end], p)
	s.pos = end
	return len(p), nil
}

func (s *seekableBuffer) Seek(offset int64, whence int) (int64, error) {
	var pos int
	switch whence {
	case io.SeekStart:
		pos = int(offset)
	case io.SeekCurrent:
		pos = s.pos + int(offset)
	case io.SeekEnd:
		pos = len(s.b) + int(offset)
	}
	if pos < 0 {
		return 0, fmt.Errorf("negative seek position: %d", pos)
	}
	s.pos = pos
	return int64(pos), nil
}

// EncodeMP4 is the fixture entry point used by tests and demo data generation.
func EncodeMP4(spec MP4Spec) ([]byte, error) {
	buf := &seekableBuffer{}
	if err := WriteMP4(buf, spec); err != nil {
		return nil, err
	}
	return buf.b, nil
}
