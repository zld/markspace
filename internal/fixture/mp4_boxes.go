package fixture

import (
	"encoding/binary"
	"os"

	"github.com/abema/go-mp4"
)

// stcoOffset records where the single chunk-offset value lives so it can
// be patched once the mdat position is known.
var stcoOffset int64

func writeMvhd(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return err
	}
	mvhd := &mp4.Mvhd{Timescale: 1000, DurationV0: 1000, Rate: 0x00010000, Volume: 0x0100,
		Matrix: [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}, NextTrackID: 2}
	if _, err := mp4.Marshal(w, mvhd, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeTkhd(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTkhd()}); err != nil {
		return err
	}
	tkhd := &mp4.Tkhd{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 3}},
		TrackID: 1, DurationV0: 1000,
		Matrix: [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
		Width:  0, Height: 0}
	if _, err := mp4.Marshal(w, tkhd, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeMdhd(w *mp4.Writer, timescale, duration uint32) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	mdhd := &mp4.Mdhd{Timescale: timescale, DurationV0: duration, Language: [3]byte{'u', 'n', 'd'}}
	if _, err := mp4.Marshal(w, mdhd, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeHdlr(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}); err != nil {
		return err
	}
	hdlr := &mp4.Hdlr{HandlerType: [4]byte{'s', 'o', 'u', 'n'}, Name: "pwf"}
	if _, err := mp4.Marshal(w, hdlr, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeSmhd(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeSmhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Smhd{}, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeDinf(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDref()}); err != nil {
		return err
	}
	dref := &mp4.Dref{FullBox: mp4.FullBox{}, EntryCount: 1}
	if _, err := mp4.Marshal(w, dref, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeUrl()}); err != nil {
		return err
	}
	url := &mp4.Url{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 1}}}
	if _, err := mp4.Marshal(w, url, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // url
		return err
	}
	if _, err := w.EndBox(); err != nil { // dref
		return err
	}
	_, err := w.EndBox() // dinf
	return err
}

func writeStbl(w *mp4.Writer, timescale, sampleDelta uint32, sizes []uint32) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	// stts: one run of identical deltas.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStts()}); err != nil {
		return err
	}
	stts := &mp4.Stts{EntryCount: 1, Entries: []mp4.SttsEntry{
		{SampleCount: uint32(len(sizes)), SampleDelta: sampleDelta},
	}}
	if _, err := mp4.Marshal(w, stts, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	// stsz: per-sample sizes.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsz()}); err != nil {
		return err
	}
	stsz := &mp4.Stsz{SampleCount: uint32(len(sizes)), EntrySize: append([]uint32(nil), sizes...)}
	if _, err := mp4.Marshal(w, stsz, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	// stco: placeholder chunk offset, patched after mdat is written.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStco()}); err != nil {
		return err
	}
	stco := &mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{0}}
	if _, err := mp4.Marshal(w, stco, mp4.Context{}); err != nil {
		return err
	}
	if pos, err := w.Seek(0, os.SEEK_CUR); err != nil {
		return err
	} else {
		stcoOffset = pos - 4 // the 4-byte ChunkOffset value
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_, err := w.EndBox() // stbl
	return err
}

func patchStco(f *os.File, chunkOffset uint32) error {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], chunkOffset)
	_, err := f.WriteAt(b[:], stcoOffset)
	return err
}
