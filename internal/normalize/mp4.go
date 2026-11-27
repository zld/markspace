package normalize

import (
	"bytes"
	"encoding/binary"
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediad/internal/ingest"
	"mediad/internal/model"
)

// matrixIdentity is the standard ISO track/display matrix.
var matrixIdentity = [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}

func fourCC(s string) [4]byte {
	var b [4]byte
	copy(b[:], s)
	return b
}

// mp4Writer carries per-file serialization state, notably the byte positions
// of stco offset entries that must be patched once mdat is laid down.
type mp4Writer struct {
	seeker    *writeSeeker
	mw        *mp4.Writer
	stcoFixes []int64
}

// WriteMP4 serializes the normalized track as a self-contained MP4 using
// go-mp4 for box framing and all time-field encoding. sampleData is the mdat
// payload (length-prefixed NAL/OBU units in track sample order).
func WriteMP4(w io.WriteSeeker, t *Track, codecConfig []byte, sampleData []byte) error {
	if t.Timescale == 0 {
		return model.New(model.ErrZeroTimebase, t.Codec, -1, "refusing to write a zero-timescale track")
	}
	state := &mp4Writer{seeker: nil, mw: mp4.NewWriter(w)}
	ctx := mp4.Context{}

	if _, err := state.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(state.mw, &mp4.Ftyp{
		MajorBrand:       fourCC("isom"),
		MinorVersion:     0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: fourCC("isom")}, {CompatibleBrand: fourCC("iso2")}},
	}, ctx); err != nil {
		return err
	}
	if _, err := state.mw.EndBox(); err != nil {
		return err
	}

	if _, err := state.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	if _, err := state.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(state.mw, &mp4.Mvhd{
		Timescale:   t.Timescale,
		DurationV0:  uint32(t.DurationTicks),
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      matrixIdentity,
		NextTrackID: 2,
	}, ctx); err != nil {
		return err
	}
	if _, err := state.mw.EndBox(); err != nil {
		return err
	}

	if _, err := state.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if err := state.writeTkhd(t, ctx); err != nil {
		return err
	}
	if err := state.writeMdia(t, codecConfig, ctx); err != nil {
		return err
	}
	if _, err := state.mw.EndBox(); err != nil { // trak
		return err
	}
	if _, err := state.mw.EndBox(); err != nil { // moov
		return err
	}

	if _, err := state.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return err
	}
	if _, err := state.mw.Write(sampleData); err != nil {
		return err
	}
	mdatBI, err := state.mw.EndBox()
	if err != nil {
		return err
	}
	// Patch every stco entry (one in this layout) to the mdat payload start.
	payloadOff := uint32(mdatBI.Offset + mdatBI.HeaderSize)
	if ws, ok := w.(*writeSeeker); ok {
		for _, pos := range state.stcoFixes {
			if _, err := ws.Seek(pos, io.SeekStart); err != nil {
				return err
			}
			var ob [4]byte
			binary.BigEndian.PutUint32(ob[:], payloadOff)
			if _, err := ws.Write(ob[:]); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *mp4Writer) writeTkhd(t *Track, ctx mp4.Context) error {
	if _, err := s.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTkhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(s.mw, &mp4.Tkhd{
		TrackID:    1,
		DurationV0: uint32(t.DurationTicks),
		Matrix:     matrixIdentity,
		Width:      uint32(t.Width) << 16,
		Height:     uint32(t.Height) << 16,
	}, ctx); err != nil {
		return err
	}
	_, err := s.mw.EndBox()
	return err
}

func (s *mp4Writer) writeMdia(t *Track, codecConfig []byte, ctx mp4.Context) error {
	if _, err := s.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if _, err := s.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(s.mw, &mp4.Mdhd{
		Timescale:  t.Timescale,
		DurationV0: uint32(t.DurationTicks),
		Language:   [3]byte{0x55, 0xC4, 0x55}, // "und"
	}, ctx); err != nil {
		return err
	}
	if _, err := s.mw.EndBox(); err != nil {
		return err
	}

	if _, err := s.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(s.mw, &mp4.Hdlr{HandlerType: fourCC("vide"), Name: "VideoHandler\000"}, ctx); err != nil {
		return err
	}
	if _, err := s.mw.EndBox(); err != nil {
		return err
	}

	if _, err := s.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if _, err := s.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeVmhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(s.mw, &mp4.Vmhd{}, ctx); err != nil {
		return err
	}
	if _, err := s.mw.EndBox(); err != nil {
		return err
	}
	if err := s.writeDinf(ctx); err != nil {
		return err
	}
	if err := s.writeStbl(t, codecConfig, ctx); err != nil {
		return err
	}
	if _, err := s.mw.EndBox(); err != nil { // minf
		return err
	}
	if _, err := s.mw.EndBox(); err != nil { // mdia
		return err
	}
	return nil
}

func (s *mp4Writer) writeDinf(ctx mp4.Context) error {
	if _, err := s.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDinf()}); err != nil {
		return err
	}
	if _, err := s.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDref()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(s.mw, &mp4.Dref{EntryCount: 1}, ctx); err != nil {
		return err
	}
	if _, err := s.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeUrl()}); err != nil {
		return err
	}
	url := &mp4.Url{}
	url.SetFlags(mp4.UrlSelfContained) // self-contained
	if _, err := mp4.Marshal(s.mw, url, ctx); err != nil {
		return err
	}
	if _, err := s.mw.EndBox(); err != nil {
		return err
	}
	if _, err := s.mw.EndBox(); err != nil { // dref
		return err
	}
	_, err := s.mw.EndBox() // dinf
	return err
}

func (s *mp4Writer) writeStbl(t *Track, codecConfig []byte, ctx mp4.Context) error {
	if _, err := s.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if err := s.writeStsd(t, codecConfig, ctx); err != nil {
		return err
	}
	delta := uint32(t.FPS.Den)
	if _, err := s.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStts()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(s.mw, &mp4.Stts{
		EntryCount: 1,
		Entries:    []mp4.SttsEntry{{SampleCount: uint32(len(t.Samples)), SampleDelta: delta}},
	}, ctx); err != nil {
		return err
	}
	if _, err := s.mw.EndBox(); err != nil {
		return err
	}
	if _, err := s.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsc()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(s.mw, &mp4.Stsc{
		EntryCount: 1,
		Entries:    []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: uint32(len(t.Samples)), SampleDescriptionIndex: 1}},
	}, ctx); err != nil {
		return err
	}
	if _, err := s.mw.EndBox(); err != nil {
		return err
	}
	sizes := make([]uint32, len(t.Samples))
	for i, sm := range t.Samples {
		sizes[i] = uint32(sm.Length)
	}
	if _, err := s.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsz()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(s.mw, &mp4.Stsz{SampleSize: 0, SampleCount: uint32(len(sizes)), EntrySize: sizes}, ctx); err != nil {
		return err
	}
	if _, err := s.mw.EndBox(); err != nil {
		return err
	}
	// stco: placeholder offset zero; patched after mdat is written.
	if _, err := s.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStco()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(s.mw, &mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{0}}, ctx); err != nil {
		return err
	}
	stcoBI, err := s.mw.EndBox()
	if err != nil {
		return err
	}
	s.stcoFixes = append(s.stcoFixes, int64(stcoBI.Offset+stcoBI.HeaderSize+8))
	if keys := t.KeySampleNumbers(); len(keys) > 0 {
		if _, err := s.mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStss()}); err != nil {
			return err
		}
		if _, err := mp4.Marshal(s.mw, &mp4.Stss{EntryCount: uint32(len(keys)), SampleNumber: keys}, ctx); err != nil {
			return err
		}
		if _, err := s.mw.EndBox(); err != nil {
			return err
		}
	}
	_, err = s.mw.EndBox() // stbl
	return err
}

// writeSeeker gives bytes.Buffer an in-place io.WriteSeeker implementation.
type writeSeeker struct {
	buf *bytes.Buffer
	pos int64
}

func (w *writeSeeker) Write(p []byte) (int, error) {
	end := w.pos + int64(len(p))
	if end > int64(w.buf.Len()) {
		w.buf.Write(make([]byte, end-int64(w.buf.Len())))
	}
	copy(w.buf.Bytes()[w.pos:end], p)
	w.pos = end
	return len(p), nil
}

func (w *writeSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		w.pos = offset
	case io.SeekCurrent:
		w.pos += offset
	case io.SeekEnd:
		w.pos = int64(w.buf.Len()) + offset
	}
	return w.pos, nil
}

// EncodeMP4 renders the canonical track to deterministic MP4 bytes.
func EncodeMP4(t *Track, codecConfig, sampleData []byte) ([]byte, error) {
	var buf bytes.Buffer
	ws := &writeSeeker{buf: &buf}
	if err := WriteMP4(ws, t, codecConfig, sampleData); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CodecConfigType returns the config box fourCC for a codec.
func CodecConfigType(codec string) string {
	switch codec {
	case ingest.CodecH264:
		return "avcC"
	case ingest.CodecH265:
		return "hvcC"
	case ingest.CodecAV1:
		return "av1C"
	default:
		return ""
	}
}
