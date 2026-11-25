package segment

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"

	mp4 "github.com/abema/go-mp4"
)

// Muxer writes fragmented MP4 output. Each segment is first written to a
// hidden temporary file in the destination directory and only renamed into
// place once fully serialized: an interrupted run therefore never leaves a
// half-written segment that a resumed run could double-commit.
type Muxer struct {
	dir string
}

// NewMuxer creates a muxer whose output lands in dir (created on demand).
func NewMuxer(dir string) (*Muxer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Muxer{dir: dir}, nil
}

// WriteInit writes the init fragment: ftyp plus a moov with a trex that
// declares the track ready for movie fragments.
func (m *Muxer) WriteInit(repID string, timescale uint32) (string, error) {
	name := repID + "-init.mp4"
	tmp, err := m.tempFile(name)
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	w := mp4.NewWriter(tmp)
	if err := writeFtyp(w); err != nil {
		return "", err
	}
	if err := writeInitMoov(w, timescale); err != nil {
		return "", err
	}
	return name, m.commit(tmp, name)
}

// WriteSegment writes one movie fragment (styp + moof + mdat). Samples are
// opaque media bytes already grouped by the cutter; only structural boxes
// are produced here.
func (m *Muxer) WriteSegment(repID string, idx int, baseMediaDecodeTime uint64, samples [][]byte) (string, int64, error) {
	name := fmt.Sprintf("%s-seg-%05d.m4s", repID, idx)
	tmp, err := m.tempFile(name)
	if err != nil {
		return "", 0, err
	}
	defer tmp.Close()
	w := mp4.NewWriter(tmp)

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("styp")}); err != nil {
		return "", 0, err
	}
	if _, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand: [4]byte{'m', 's', 'd', 'h'},
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'m', 's', 'd', 'h'}},
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
		},
	}, mp4.Context{}); err != nil {
		return "", 0, err
	}
	if _, err = w.EndBox(); err != nil {
		return "", 0, err
	}

	// moof
	moof, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoof()})
	if err != nil {
		return "", 0, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMfhd()}); err != nil {
		return "", 0, err
	}
	if _, err := mp4.Marshal(w, &mp4.Mfhd{SequenceNumber: uint32(idx + 1)}, mp4.Context{}); err != nil {
		return "", 0, err
	}
	if _, err := w.EndBox(); err != nil {
		return "", 0, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTraf()}); err != nil {
		return "", 0, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTfhd()}); err != nil {
		return "", 0, err
	}
	if _, err := mp4.Marshal(w, &mp4.Tfhd{
		FullBox: mp4.FullBox{Flags: [3]byte{0x02, 0x00, 0x00}}, // default-base-is-moof
		TrackID: 1,
	}, mp4.Context{}); err != nil {
		return "", 0, err
	}
	if _, err := w.EndBox(); err != nil {
		return "", 0, err
	}
	// version-1 tfdt keeps 64-bit decode time to survive long timelines.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTfdt()}); err != nil {
		return "", 0, err
	}
	if _, err := mp4.Marshal(w, &mp4.Tfdt{
		FullBox:               mp4.FullBox{Version: 1},
		BaseMediaDecodeTimeV1: baseMediaDecodeTime,
	}, mp4.Context{}); err != nil {
		return "", 0, err
	}
	if _, err := w.EndBox(); err != nil {
		return "", 0, err
	}
	trunStart, err := w.Seek(0, io.SeekCurrent)
	if err != nil {
		return "", 0, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrun()}); err != nil {
		return "", 0, err
	}
	entries := make([]mp4.TrunEntry, len(samples))
	var payloadBytes int
	for i, s := range samples {
		entries[i] = mp4.TrunEntry{SampleSize: uint32(len(s))}
		payloadBytes += len(s)
	}
	trun := &mp4.Trun{
		FullBox:     mp4.FullBox{Flags: [3]byte{0x00, 0x02, 0x01}}, // data-offset + sample-size
		SampleCount: uint32(len(samples)),
		Entries:     entries,
	}
	if _, err := mp4.Marshal(w, trun, mp4.Context{}); err != nil {
		return "", 0, err
	}
	trunBox, err := w.EndBox()
	if err != nil {
		return "", 0, err
	}
	if _, err := w.EndBox(); err != nil { // traf
		return "", 0, err
	}
	if _, err := w.EndBox(); err != nil { // moof
		return "", 0, err
	}

	// mdat: header followed by the concatenated sample payload.
	mdat, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()})
	if err != nil {
		return "", 0, err
	}
	for _, s := range samples {
		if _, err := w.Write(s); err != nil {
			return "", 0, err
		}
	}
	if _, err := w.EndBox(); err != nil {
		return "", 0, err
	}

	// Patch trun.data_offset = mdat.payloadStart - moof.start.
	dataOffset := int32(int(mdat.Offset) + int(mdat.HeaderSize) - int(moof.Offset))
	if err := patchTrunDataOffset(tmp, trunStart, trunBox.HeaderSize, dataOffset); err != nil {
		return "", 0, err
	}
	size, err := tmp.Seek(0, io.SeekEnd)
	if err != nil {
		return "", 0, err
	}
	if err := m.commit(tmp, name); err != nil {
		return "", 0, err
	}
	return name, size, nil
}

// tempFile creates a hidden temp file in the output directory so the rename
// is guaranteed to be on the same filesystem.
func (m *Muxer) tempFile(final string) (*os.File, error) {
	tmpName := ".tmp-" + final
	return os.OpenFile(filepath.Join(m.dir, tmpName),
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
}

// commit flushes, closes and atomically renames the temp file to its final
// name. tmp is closed here; callers also defer Close, which then is a no-op.
func (m *Muxer) commit(tmp *os.File, final string) error {
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(filepath.Join(m.dir, ".tmp-"+final), filepath.Join(m.dir, final))
}

// patchTrunDataOffset seeks to the trun box's data-offset field and writes
// the resolved value in place. Layout after the box header: version+flags
// (4), sample_count (4), then data_offset (4, present via flag 0x000001).
func patchTrunDataOffset(f *os.File, trunStart int64, headerSize uint64, offset int32) error {
	pos := trunStart + int64(headerSize) + 4 + 4
	if _, err := f.Seek(pos, io.SeekStart); err != nil {
		return err
	}
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(offset))
	_, err := f.Write(buf[:])
	return err
}

func writeFtyp(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	_, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand: [4]byte{'i', 's', 'o', 'm'},
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'i', 's', 'o', '4'}},
		},
	}, mp4.Context{})
	if err != nil {
		return err
	}
	_, err = w.EndBox()
	return err
}

func writeInitMoov(w *mp4.Writer, timescale uint32) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Mvhd{
		Timescale: timescale, DurationV0: 0,
		NextTrackID: 2,
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTkhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Tkhd{
		FullBox:    mp4.FullBox{Flags: [3]byte{0x00, 0x00, 0x03}}, // enabled+in-movie
		TrackID:    1,
		DurationV0: 0,
		Width:      320 << 16, Height: 180 << 16,
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Mdhd{
		Timescale: timescale, DurationV0: 0, Language: [3]byte{'u', 'n', 'd'},
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "mediad"}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeVmhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Vmhd{}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDref()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Dref{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeUrl()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Url{FullBox: mp4.FullBox{Flags: [3]byte{0x00, 0x00, 0x01}}}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // url
		return err
	}
	if _, err := w.EndBox(); err != nil { // dref
		return err
	}
	if _, err := w.EndBox(); err != nil { // dinf
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	for _, t := range []string{"stsd", "stts", "stsc", "stsz", "stco"} {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType(t)}); err != nil {
			return err
		}
		switch t {
		case "stsd":
			if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 0}, mp4.Context{}); err != nil {
				return err
			}
		case "stts":
			if _, err := mp4.Marshal(w, &mp4.Stts{EntryCount: 0}, mp4.Context{}); err != nil {
				return err
			}
		case "stsc":
			if _, err := mp4.Marshal(w, &mp4.Stsc{EntryCount: 0}, mp4.Context{}); err != nil {
				return err
			}
		case "stsz":
			if _, err := mp4.Marshal(w, &mp4.Stsz{SampleSize: 0, SampleCount: 0}, mp4.Context{}); err != nil {
				return err
			}
		case "stco":
			if _, err := mp4.Marshal(w, &mp4.Stco{EntryCount: 0}, mp4.Context{}); err != nil {
				return err
			}
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
	}
	if _, err := w.EndBox(); err != nil { // stbl
		return err
	}
	if _, err := w.EndBox(); err != nil { // minf
		return err
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return err
	}
	if _, err := w.EndBox(); err != nil { // trak
		return err
	}
	// mvex/trex declares the track as fragmented.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mvex")}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrex()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Trex{
		TrackID:                       1,
		DefaultSampleDescriptionIndex: 1,
		DefaultSampleDuration:         0,
		DefaultSampleSize:             0,
		DefaultSampleFlags:            0,
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // trex
		return err
	}
	if _, err := w.EndBox(); err != nil { // mvex
		return err
	}
	_, err := w.EndBox() // moov
	return err
}
