package medpipe

import (
	"io"

	mp4 "github.com/abema/go-mp4"
)

// ReadWriteSeeker is the storage capability fragment files require: the
// trun data offset must be patched in place after the moof is serialized.
type ReadWriteSeeker interface {
	io.Reader
	io.Writer
	io.Seeker
}

// WriteTFRA serializes the index as an ISO-BMFF track fragment random access
// box (tfra, ISO/IEC 14496-12 8.8.22). Each keyframe entry maps its unified
// presentation time to a byte offset in the capture, giving standard MP4
// tooling a random-access view without copying the coded samples.
func WriteTFRA(w io.WriteSeeker, trackID uint32, idx *TrackIndex) error {
	mw := mp4.NewWriter(w)
	bi, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMfra()})
	if err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTfra()}); err != nil {
		return err
	}
	tfra := &mp4.Tfra{TrackID: trackID}
	// Version 1 gives 64-bit time and offset fields, which the unified
	// microsecond axis and large captures require.
	tfra.SetVersion(1)
	// Traf/trun/sample numbers fit in one byte each (length size 0).
	for _, e := range idx.Keyframes() {
		tfra.Entries = append(tfra.Entries, mp4.TfraEntry{
			TimeV1:       uint64(e.UnifiedUS),
			MoofOffsetV1: uint64(e.ByteOffset),
			TrafNumber:   1,
			TrunNumber:   1,
			SampleNumber: uint32(e.Ordinal + 1),
		})
	}
	tfra.NumberOfEntry = uint32(len(tfra.Entries))
	if _, err := mp4.Marshal(mw, tfra, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil { // tfra
		return err
	}
	if _, err := writeMFRO(mw, bi); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil { // mfra
		return err
	}
	return nil
}

// writeMFRO appends the mandatory mfra size box, whose value is the total
// size of the enclosing mfra container.
func writeMFRO(mw *mp4.Writer, mfraInfo *mp4.BoxInfo) (*mp4.BoxInfo, error) {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMfro()}); err != nil {
		return nil, err
	}
	// The container size is finalized by EndBox afterwards; mfro only needs
	// a placeholder field that the marshaller writes as 4 bytes.
	if _, err := mp4.Marshal(mw, &mp4.Mfro{Size: 0}, mp4.Context{}); err != nil {
		return nil, err
	}
	return mw.EndBox()
}

// Fragment describes one movie fragment for WriteMoof: a decode time plus the
// samples belonging to the fragment, each with duration and size.
type Fragment struct {
	Sequence     uint32
	TrackID      uint32
	DecodeTimeUS uint64 // base media decode time in unified microseconds
	Samples      []FragmentSample
}

// FragmentSample is one sample row inside a trun box.
type FragmentSample struct {
	DurationUS uint32
	Size       uint32
	IsKeyframe bool
}

// WriteMoof writes one moof/traf/tfdt/trun fragment followed by an mdat
// header. The caller writes sample bytes to w afterwards; dataOffset in trun
// points at the first sample relative to the moof start. Times use the
// unified microsecond time base (tfdt version 1).
func WriteMoof(w ReadWriteSeeker, f Fragment, payloadSize uint64) error {
	mw := mp4.NewWriter(w)
	moofStart, err := w.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoof()}); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMfhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Mfhd{SequenceNumber: f.Sequence}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTraf()}); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTfhd()}); err != nil {
		return err
	}
	tfhd := &mp4.Tfhd{TrackID: f.TrackID}
	tfhd.SetFlags(mp4.TfhdDefaultBaseIsMoof)
	if _, err := mp4.Marshal(mw, tfhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTfdt()}); err != nil {
		return err
	}
	tfdt := &mp4.Tfdt{BaseMediaDecodeTimeV1: f.DecodeTimeUS}
	tfdt.SetVersion(1)
	if _, err := mp4.Marshal(mw, tfdt, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrun()}); err != nil {
		return err
	}
	trun := &mp4.Trun{SampleCount: uint32(len(f.Samples))}
	// Flags: data-offset present (0x1), sample duration (0x100),
	// sample size (0x200), sample flags (0x400).
	trun.SetFlags(0x000001 | 0x000100 | 0x000200 | 0x000400)
	for _, s := range f.Samples {
		var flags uint32 // 0x010000 = sample_depends_on_other (non-keyframe)
		if !s.IsKeyframe {
			flags = 0x010000
		}
		trun.Entries = append(trun.Entries, mp4.TrunEntry{
			SampleDuration: s.DurationUS,
			SampleSize:     s.Size,
			SampleFlags:    flags,
		})
	}
	if _, err := mp4.Marshal(mw, trun, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil { // trun
		return err
	}
	if _, err := mw.EndBox(); err != nil { // traf
		return err
	}
	moofInfo, err := mw.EndBox()
	if err != nil {
		return err
	}
	// Fix the trun data offset: distance from the moof start to the first
	// mdat payload byte, i.e. full moof size plus the 8-byte mdat header.
	dataOffset := int32(moofInfo.Size) + 8
	trun.DataOffset = dataOffset
	if _, err := w.Seek(moofStart, io.SeekStart); err != nil {
		return err
	}
	if err := patchTrunDataOffset(w, f, dataOffset); err != nil {
		return err
	}
	if _, err := w.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	return writeMdatHeader(w, payloadSize)
}
