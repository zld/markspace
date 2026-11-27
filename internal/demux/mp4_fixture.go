package demux

import (
	"bytes"
	"io"

	"github.com/abema/go-mp4"

	"mediarc/internal/model"
)

// FixtureSample is one generated compressed sample.
type FixtureSample struct {
	Dts      uint64
	Duration uint32
	Payload  []byte
	Sync     bool
}

// WriteFragmentedMP4 builds a minimal but standards-shaped fragmented MP4
// (init moov with one video track + one or more moof/mdat fragments). The
// generated file round-trips through MP4Demuxer in tests.
func WriteFragmentedMP4(timescale uint32, trackID uint32, handler string, fragments [][]FixtureSample) ([]byte, error) {
	buf := &writeSeeker{buf: &bytes.Buffer{}}
	w := mp4.NewWriter(buf)

	// ftyp
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand: [4]byte{'i', 's', 'o', 'm'}, MinorVersion: 0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}},
	}, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	// moov > mvhd/trak/mvex
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return nil, err
	}
	if err := marshalBox(w, mp4.BoxTypeMvhd(), &mp4.Mvhd{
		Timescale: timescale, DurationV0: 0xFFFFFFFF,
		NextTrackID: trackID + 1,
	}); err != nil {
		return nil, err
	}
	if err := writeTrak(w, trackID, timescale, handler); err != nil {
		return nil, err
	}
	if err := writeMvex(w, trackID); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	// media fragments; baseMediaDecodeTime is the cumulative sample duration
	// of all preceding fragments, as required by the fragmented MP4 timeline.
	var baseDts uint64
	for _, samples := range fragments {
		if err := writeFragment(w, trackID, baseDts, samples); err != nil {
			return nil, err
		}
		for _, s := range samples {
			baseDts += uint64(s.Duration)
		}
	}
	return append([]byte(nil), buf.buf.Bytes()...), nil
}

func writeTrak(w *mp4.Writer, trackID uint32, timescale uint32, handler string) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if err := marshalBox(w, mp4.BoxTypeTkhd(), &mp4.Tkhd{
		TrackID: trackID, DurationV0: 0xFFFFFFFF,
		Width:  1 << 16,
		Height: 1 << 16,
	}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if err := marshalBox(w, mp4.BoxTypeMdhd(), &mp4.Mdhd{Timescale: timescale, DurationV0: 0xFFFFFFFF}); err != nil {
		return err
	}
	var ht [4]byte
	copy(ht[:], handler)
	if err := marshalBox(w, mp4.BoxTypeHdlr(), &mp4.Hdlr{HandlerType: ht, Name: handler + " handler"}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if err := marshalBox(w, mp4.BoxTypeVmhd(), &mp4.Vmhd{}); err != nil {
		return err
	}
	if err := writeDinf(w); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if err := marshalBox(w, mp4.BoxTypeStsd(), &mp4.Stsd{}); err != nil {
		return err
	}
	if err := marshalBox(w, mp4.BoxTypeStts(), &mp4.Stts{}); err != nil {
		return err
	}
	if err := marshalBox(w, mp4.BoxTypeStsc(), &mp4.Stsc{}); err != nil {
		return err
	}
	if err := marshalBox(w, mp4.BoxTypeStsz(), &mp4.Stsz{SampleSize: 0}); err != nil {
		return err
	}
	if err := marshalBox(w, mp4.BoxTypeStco(), &mp4.Stco{}); err != nil {
		return err
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
	return nil
}

func writeDinf(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDref()}); err != nil {
		return err
	}
	url := &mp4.Url{}
	url.SetFlags(0x000001) // self-contained flag
	if _, err := mp4.Marshal(w, url, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // dref
		return err
	}
	if _, err := w.EndBox(); err != nil { // dinf
		return err
	}
	return nil
}

func writeMvex(w *mp4.Writer, trackID uint32) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvex()}); err != nil {
		return err
	}
	if err := marshalBox(w, mp4.BoxTypeMfhd(), &mp4.Mfhd{SequenceNumber: 1}); err != nil {
		return err
	}
	if err := marshalBox(w, mp4.BoxTypeTrex(), &mp4.Trex{TrackID: trackID, DefaultSampleDescriptionIndex: 1}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	return nil
}

func writeFragment(w *mp4.Writer, trackID uint32, baseDts uint64, samples []FixtureSample) error {
	// moof
	moofStart, err := w.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoof()}); err != nil {
		return err
	}
	if err := marshalBox(w, mp4.BoxTypeMfhd(), &mp4.Mfhd{SequenceNumber: 1}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTraf()}); err != nil {
		return err
	}
	if err := marshalBox(w, mp4.BoxTypeTfhd(), &mp4.Tfhd{TrackID: trackID}); err != nil {
		return err
	}
	if err := marshalBox(w, mp4.BoxTypeTfdt(), &mp4.Tfdt{
		FullBox:               mp4.FullBox{Version: 1},
		BaseMediaDecodeTimeV1: baseDts,
	}); err != nil {
		return err
	}
	// trun with data-offset + duration + size flags; data offset patched after
	// the moof header size is known.
	trun := &mp4.Trun{SampleCount: uint32(len(samples))}
	trun.AddFlag(0x000001 | 0x000100 | 0x000200 | 0x000400) // data-offset, duration, size, flags
	trun.Entries = make([]mp4.TrunEntry, len(samples))
	for i, s := range samples {
		var flags uint32
		if !s.Sync {
			flags = 0x10000 // sample_is_non_sync_sample
		}
		trun.Entries[i] = mp4.TrunEntry{
			SampleDuration: s.Duration, SampleSize: uint32(len(s.Payload)), SampleFlags: flags,
		}
	}
	if err := marshalBox(w, mp4.BoxTypeTrun(), trun); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // traf
		return err
	}
	moofEnd, err := w.EndBox() // moof
	if err != nil {
		return err
	}
	moofSize := int64(moofEnd.Size)

	// mdat payload follows immediately; data offset is relative to moof start.
	mdatHeader := 8
	totalSampleBytes := 0
	for _, s := range samples {
		totalSampleBytes += len(s.Payload)
	}
	dataOffset := moofSize + int64(mdatHeader)
	trun.DataOffset = int32(dataOffset)

	// Patch the trun box in place by rewriting the whole moof region.
	if _, err := w.Seek(moofStart, io.SeekStart); err != nil {
		return err
	}
	if err := rewriteMoof(w, trackID, baseDts, trun); err != nil {
		return err
	}
	if _, err := w.Seek(moofStart+moofSize, io.SeekStart); err != nil {
		return err
	}

	// mdat
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return err
	}
	for _, s := range samples {
		if _, err := w.Write(s.Payload); err != nil {
			return err
		}
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = totalSampleBytes
	return nil
}

func rewriteMoof(w *mp4.Writer, trackID uint32, baseDts uint64, trun *mp4.Trun) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoof()}); err != nil {
		return err
	}
	if err := marshalBox(w, mp4.BoxTypeMfhd(), &mp4.Mfhd{SequenceNumber: 1}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTraf()}); err != nil {
		return err
	}
	if err := marshalBox(w, mp4.BoxTypeTfhd(), &mp4.Tfhd{TrackID: trackID}); err != nil {
		return err
	}
	if err := marshalBox(w, mp4.BoxTypeTfdt(), &mp4.Tfdt{
		FullBox:               mp4.FullBox{Version: 1},
		BaseMediaDecodeTimeV1: baseDts,
	}); err != nil {
		return err
	}
	if err := marshalBox(w, mp4.BoxTypeTrun(), trun); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // traf
		return err
	}
	if _, err := w.EndBox(); err != nil { // moof
		return err
	}
	return nil
}

func marshalBox(w *mp4.Writer, bt mp4.BoxType, box mp4.IImmutableBox) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: bt}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, box, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	return nil
}

// writeSeeker adapts bytes.Buffer to io.WriteSeeker.
type writeSeeker struct {
	buf *bytes.Buffer
	pos int64
}

func (ws *writeSeeker) Write(p []byte) (int, error) {
	end := ws.pos + int64(len(p))
	if end > int64(ws.buf.Len()) {
		need := end - int64(ws.buf.Len())
		ws.buf.Write(make([]byte, need))
	}
	b := ws.buf.Bytes()
	copy(b[ws.pos:end], p)
	ws.pos = end
	return len(p), nil
}

func (ws *writeSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		ws.pos = offset
	case io.SeekCurrent:
		ws.pos += offset
	case io.SeekEnd:
		ws.pos = int64(ws.buf.Len()) + offset
	}
	if ws.pos < 0 {
		return 0, io.EOF
	}
	return ws.pos, nil
}

// SamplesToTrack converts generated samples into a model.Track shape for
// fingerprint/remux tests.
func SamplesToTrack(id uint32, codec model.Codec, timescale uint32, samples []FixtureSample) model.Track {
	tr := model.Track{ID: id, Codec: codec, Timescale: timescale}
	for _, s := range samples {
		tr.Samples = append(tr.Samples, model.Sample{
			Dts: s.Dts, Duration: s.Duration, Payload: append([]byte(nil), s.Payload...), Sync: s.Sync,
		})
	}
	return tr
}
