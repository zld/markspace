package ingest

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	gomp4 "github.com/abema/go-mp4"

	"mediad/internal/mederr"
)

// FixtureTrack is one generated supported PCM track muxed into a sample MP4.
type FixtureTrack struct {
	ID       uint32
	Rate     int
	Channels int
	PCM      []byte // interleaved signed 16-bit little-endian samples
}

// WriteFixtureMP4 writes a deterministic MP4 with one or more audio tracks.
// It uses go-mp4's box writer for all standard timeline/sample boxes. The
// mdat payload is the project-generated s16 PCM fixture.
func WriteFixtureMP4(path string, tracks []FixtureTrack) error {
	if len(tracks) == 0 || len(tracks) > 8 {
		return mederr.New(mederr.CatLimit, "mp4", -1, -1, false, "MP4 fixture needs 1..8 tracks")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return mederr.New(mederr.CatIO, "mp4", -1, -1, false, "create fixture dir: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return mederr.New(mederr.CatIO, "mp4", -1, -1, false, "create MP4 fixture: %v", err)
	}
	defer f.Close()
	w := gomp4.NewWriter(f)

	if err := writeFTyp(w); err != nil {
		return err
	}

	offsets := make([]uint32, len(tracks))
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMdat()}); err != nil {
		return mp4WriteErr("mdat-start", err)
	}
	for i, tr := range tracks {
		if tr.Rate <= 0 || tr.Channels <= 0 || len(tr.PCM)%(2*tr.Channels) != 0 {
			return mederr.New(mederr.CatAudio, fmt.Sprintf("mp4:%d", tr.ID), -1, -1, false,
				"invalid PCM fixture: rate=%d channels=%d bytes=%d", tr.Rate, tr.Channels, len(tr.PCM))
		}
		pos, err := w.Seek(0, io.SeekCurrent)
		if err != nil {
			return mp4WriteErr("mdat-offset", err)
		}
		if pos < 0 || pos > math.MaxUint32 {
			return mederr.New(mederr.CatLimit, fmt.Sprintf("mp4:%d", tr.ID), -1, -1, false, "mdat offset too large")
		}
		offsets[i] = uint32(pos)
		if _, err := w.Write(tr.PCM); err != nil {
			return mp4WriteErr("mdat-write", err)
		}
	}
	if _, err := w.EndBox(); err != nil {
		return mp4WriteErr("mdat-end", err)
	}

	var durationMs uint32
	for _, tr := range tracks {
		frames := uint64(len(tr.PCM) / (2 * tr.Channels))
		ms := frames * 1000 / uint64(tr.Rate)
		if ms > math.MaxUint32 {
			return mederr.New(mederr.CatLimit, fmt.Sprintf("mp4:%d", tr.ID), -1, -1, false, "duration too large")
		}
		if uint32(ms) > durationMs {
			durationMs = uint32(ms)
		}
	}
	if err := writeMoov(w, tracks, offsets, durationMs); err != nil {
		return err
	}
	return f.Close()
}

func writeFTyp(w *gomp4.Writer) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeFtyp()}); err != nil {
		return mp4WriteErr("ftyp-start", err)
	}
	ft := &gomp4.Ftyp{MajorBrand: [4]byte{'i', 's', 'o', 'm'}, MinorVersion: 512}
	ft.AddCompatibleBrand([4]byte{'i', 's', 'o', 'm'})
	ft.AddCompatibleBrand([4]byte{'m', 'p', '4', '1'})
	if _, err := gomp4.Marshal(w, ft, gomp4.Context{}); err != nil {
		return mp4WriteErr("ftyp", err)
	}
	_, err := w.EndBox()
	return mp4WriteErr("ftyp-end", err)
}

func writeMoov(w *gomp4.Writer, tracks []FixtureTrack, offsets []uint32, durationMs uint32) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMoov()}); err != nil {
		return mp4WriteErr("moov-start", err)
	}
	matrix := [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}
	mvhd := &gomp4.Mvhd{
		Timescale:   1000,
		DurationV0:  durationMs,
		Rate:        0x10000,
		Volume:      0x0100,
		Matrix:      matrix,
		NextTrackID: uint32(len(tracks) + 1),
	}
	if err := marshalBox(w, gomp4.BoxTypeMvhd(), mvhd); err != nil {
		return err
	}
	for i, tr := range tracks {
		if err := writeTrak(w, tr, offsets[i], durationMs, matrix); err != nil {
			return err
		}
	}
	_, err := w.EndBox()
	return mp4WriteErr("moov-end", err)
}

func writeTrak(w *gomp4.Writer, tr FixtureTrack, offset uint32, durationMs uint32, matrix [9]int32) error {
	frames := uint32(len(tr.PCM) / (2 * tr.Channels))
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeTrak()}); err != nil {
		return mp4WriteErr("trak-start", err)
	}
	tkhd := &gomp4.Tkhd{
		FullBox:    gomp4.FullBox{Flags: [3]byte{0, 0, 3}},
		TrackID:    tr.ID,
		DurationV0: durationMs,
		Volume:     0x0100,
		Matrix:     matrix,
	}
	if err := marshalBox(w, gomp4.BoxTypeTkhd(), tkhd); err != nil {
		return err
	}
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMdia()}); err != nil {
		return mp4WriteErr("mdia-start", err)
	}
	mdhd := &gomp4.Mdhd{Timescale: uint32(tr.Rate), DurationV0: frames}
	if err := marshalBox(w, gomp4.BoxTypeMdhd(), mdhd); err != nil {
		return err
	}
	hdlr := &gomp4.Hdlr{HandlerType: [4]byte{'s', 'o', 'u', 'n'}, Name: "mediad-pcm"}
	if err := marshalBox(w, gomp4.BoxTypeHdlr(), hdlr); err != nil {
		return err
	}
	if err := writeMinf(w, tr, offset, frames); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return mp4WriteErr("mdia-end", err)
	}
	_, err := w.EndBox() // trak
	return mp4WriteErr("trak-end", err)
}

func writeMinf(w *gomp4.Writer, tr FixtureTrack, offset, frames uint32) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMinf()}); err != nil {
		return mp4WriteErr("minf-start", err)
	}
	if err := marshalBox(w, gomp4.BoxTypeSmhd(), &gomp4.Smhd{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeDinf()}); err != nil {
		return mp4WriteErr("dinf-start", err)
	}
	dref := &gomp4.Dref{FullBox: gomp4.FullBox{Flags: [3]byte{0, 0, 1}}, EntryCount: 1}
	if err := marshalBox(w, gomp4.BoxTypeDref(), dref); err != nil {
		return err
	}
	url := &gomp4.Url{FullBox: gomp4.FullBox{Flags: [3]byte{0, 0, 1}}}
	if err := marshalBox(w, gomp4.BoxTypeUrl(), url); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // dinf
		return mp4WriteErr("dinf-end", err)
	}
	if err := writeStbl(w, tr, offset, frames); err != nil {
		return err
	}
	_, err := w.EndBox() // minf
	return mp4WriteErr("minf-end", err)
}

func writeStbl(w *gomp4.Writer, tr FixtureTrack, offset, frames uint32) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeStbl()}); err != nil {
		return mp4WriteErr("stbl-start", err)
	}
	stsd := &gomp4.Stsd{EntryCount: 1}
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeStsd()}); err != nil {
		return mp4WriteErr("stsd-start", err)
	}
	if _, err := gomp4.Marshal(w, stsd, gomp4.Context{}); err != nil {
		return mp4WriteErr("stsd", err)
	}
	ase := &gomp4.AudioSampleEntry{
		SampleEntry: gomp4.SampleEntry{
			AnyTypeBox:         gomp4.AnyTypeBox{Type: gomp4.BoxTypeMp4a()},
			DataReferenceIndex: 1,
		},
		ChannelCount: uint16(tr.Channels),
		SampleSize:   16,
		SampleRate:   uint32(tr.Rate) << 16,
	}
	if err := marshalBox(w, gomp4.BoxTypeMp4a(), ase); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // stsd
		return mp4WriteErr("stsd-end", err)
	}
	stts := &gomp4.Stts{EntryCount: 1, Entries: []gomp4.SttsEntry{{SampleCount: 1, SampleDelta: frames}}}
	if err := marshalBox(w, gomp4.BoxTypeStts(), stts); err != nil {
		return err
	}
	stsc := &gomp4.Stsc{EntryCount: 1, Entries: []gomp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1}}}
	if err := marshalBox(w, gomp4.BoxTypeStsc(), stsc); err != nil {
		return err
	}
	stsz := &gomp4.Stsz{SampleSize: 0, SampleCount: 1, EntrySize: []uint32{uint32(len(tr.PCM))}}
	if err := marshalBox(w, gomp4.BoxTypeStsz(), stsz); err != nil {
		return err
	}
	stco := &gomp4.Stco{EntryCount: 1, ChunkOffset: []uint32{offset}}
	if err := marshalBox(w, gomp4.BoxTypeStco(), stco); err != nil {
		return err
	}
	_, err := w.EndBox() // stbl
	return mp4WriteErr("stbl-end", err)
}

func marshalBox(w *gomp4.Writer, typ gomp4.BoxType, payload gomp4.IImmutableBox) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: typ}); err != nil {
		return mp4WriteErr(typ.String()+"-start", err)
	}
	if _, err := gomp4.Marshal(w, payload, gomp4.Context{}); err != nil {
		return mp4WriteErr(typ.String(), err)
	}
	_, err := w.EndBox()
	return mp4WriteErr(typ.String()+"-end", err)
}

func mp4WriteErr(box string, err error) error {
	if err == nil {
		return nil
	}
	return mederr.New(mederr.CatParse, "mp4", -1, -1, false, "write %s box: %v", box, err)
}
