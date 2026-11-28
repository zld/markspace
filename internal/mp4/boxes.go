package mp4

import (
	gomp4 "github.com/abema/go-mp4"

	"rtp-rebuild/internal/clock"
	"rtp-rebuild/internal/fault"
	"rtp-rebuild/internal/rtp"
)

// writeMoov encodes the complete moov box into buf and fills samples with the
// chunk-relative layout offsets (later shifted by the mdat offset).
func writeMoov(buf *writer, packets []rtp.Packet, durations []uint32, opts WriteOptions, samples *[]Sample) (int64, error) {
	w := gomp4.NewWriter(buf)
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMoov()}); err != nil {
		return 0, fault.New(fault.StageMux, "start moov", err)
	}
	if err := marshalMvhd(w, durations); err != nil {
		return 0, err
	}
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeTrak()}); err != nil {
		return 0, fault.New(fault.StageMux, "start trak", err)
	}
	if err := marshalTkhd(w, durations, opts); err != nil {
		return 0, err
	}
	if err := writeMdia(w, packets, durations, opts, samples); err != nil {
		return 0, err
	}
	if _, err := w.EndBox(); err != nil { // trak
		return 0, fault.New(fault.StageMux, "end trak", err)
	}
	if _, err := w.EndBox(); err != nil { // moov
		return 0, fault.New(fault.StageMux, "end moov", err)
	}
	return buf.pos, nil
}

func totalDuration(durations []uint32) uint64 {
	var total uint64
	for _, d := range durations {
		total += uint64(d)
	}
	return total
}

func marshalMvhd(w *gomp4.Writer, durations []uint32) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMvhd()}); err != nil {
		return fault.New(fault.StageMux, "start mvhd", err)
	}
	mvhd := &gomp4.Mvhd{
		Timescale:   clock.TickRate,
		Rate:        0x00010000,
		Matrix:      identityMatrix(),
		NextTrackID: 2,
	}
	if d := totalDuration(durations); d <= 0xFFFFFFFF {
		mvhd.DurationV0 = uint32(d)
	} else {
		mvhd.SetVersion(1)
		mvhd.DurationV1 = d
	}
	if _, err := gomp4.Marshal(w, mvhd, gomp4.Context{}); err != nil {
		return fault.New(fault.StageMux, "marshal mvhd", err)
	}
	if _, err := w.EndBox(); err != nil {
		return fault.New(fault.StageMux, "end mvhd", err)
	}
	return nil
}

func marshalTkhd(w *gomp4.Writer, durations []uint32, opts WriteOptions) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeTkhd()}); err != nil {
		return fault.New(fault.StageMux, "start tkhd", err)
	}
	tkhd := &gomp4.Tkhd{
		TrackID: 1,
		Matrix:  identityMatrix(),
		Width:   uint32(opts.Width) << 16,
		Height:  uint32(opts.Height) << 16,
	}
	tkhd.Flags = [3]byte{0x00, 0x00, 0x03} // enabled | in movie
	if d := totalDuration(durations); d <= 0xFFFFFFFF {
		tkhd.DurationV0 = uint32(d)
	} else {
		tkhd.SetVersion(1)
		tkhd.DurationV1 = d
	}
	if _, err := gomp4.Marshal(w, tkhd, gomp4.Context{}); err != nil {
		return fault.New(fault.StageMux, "marshal tkhd", err)
	}
	if _, err := w.EndBox(); err != nil {
		return fault.New(fault.StageMux, "end tkhd", err)
	}
	return nil
}

func identityMatrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}

func writeMdia(w *gomp4.Writer, packets []rtp.Packet, durations []uint32, opts WriteOptions, samples *[]Sample) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMdia()}); err != nil {
		return fault.New(fault.StageMux, "start mdia", err)
	}
	if err := writeMdhd(w, durations); err != nil {
		return err
	}
	if err := writeHdlr(w); err != nil {
		return err
	}
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMinf()}); err != nil {
		return fault.New(fault.StageMux, "start minf", err)
	}
	if err := writeVmhd(w); err != nil {
		return err
	}
	if err := writeDinf(w); err != nil {
		return err
	}
	if err := writeStbl(w, packets, durations, opts, samples); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // minf
		return fault.New(fault.StageMux, "end minf", err)
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return fault.New(fault.StageMux, "end mdia", err)
	}
	return nil
}

func writeMdhd(w *gomp4.Writer, durations []uint32) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMdhd()}); err != nil {
		return fault.New(fault.StageMux, "start mdhd", err)
	}
	mdhd := &gomp4.Mdhd{
		Timescale: clock.TickRate,
		Language:  [3]byte{'u', 'n', 'd'},
	}
	if d := totalDuration(durations); d <= 0xFFFFFFFF {
		mdhd.DurationV0 = uint32(d)
	} else {
		mdhd.SetVersion(1)
		mdhd.DurationV1 = d
	}
	if _, err := gomp4.Marshal(w, mdhd, gomp4.Context{}); err != nil {
		return fault.New(fault.StageMux, "marshal mdhd", err)
	}
	if _, err := w.EndBox(); err != nil {
		return fault.New(fault.StageMux, "end mdhd", err)
	}
	return nil
}

func writeHdlr(w *gomp4.Writer) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeHdlr()}); err != nil {
		return fault.New(fault.StageMux, "start hdlr", err)
	}
	hdlr := &gomp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "VideoHandler"}
	if _, err := gomp4.Marshal(w, hdlr, gomp4.Context{}); err != nil {
		return fault.New(fault.StageMux, "marshal hdlr", err)
	}
	if _, err := w.EndBox(); err != nil {
		return fault.New(fault.StageMux, "end hdlr", err)
	}
	return nil
}

func writeVmhd(w *gomp4.Writer) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeVmhd()}); err != nil {
		return fault.New(fault.StageMux, "start vmhd", err)
	}
	if _, err := gomp4.Marshal(w, &gomp4.Vmhd{}, gomp4.Context{}); err != nil {
		return fault.New(fault.StageMux, "marshal vmhd", err)
	}
	if _, err := w.EndBox(); err != nil {
		return fault.New(fault.StageMux, "end vmhd", err)
	}
	return nil
}

func writeDinf(w *gomp4.Writer) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeDinf()}); err != nil {
		return fault.New(fault.StageMux, "start dinf", err)
	}
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeDref()}); err != nil {
		return fault.New(fault.StageMux, "start dref", err)
	}
	if _, err := gomp4.Marshal(w, &gomp4.Dref{EntryCount: 1}, gomp4.Context{}); err != nil {
		return fault.New(fault.StageMux, "marshal dref", err)
	}
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeUrl()}); err != nil {
		return fault.New(fault.StageMux, "start url", err)
	}
	url := &gomp4.Url{}
	url.Flags = [3]byte{0x00, 0x00, 0x01} // self-contained
	if _, err := gomp4.Marshal(w, url, gomp4.Context{}); err != nil {
		return fault.New(fault.StageMux, "marshal url", err)
	}
	if _, err := w.EndBox(); err != nil {
		return fault.New(fault.StageMux, "end url", err)
	}
	if _, err := w.EndBox(); err != nil {
		return fault.New(fault.StageMux, "end dref", err)
	}
	if _, err := w.EndBox(); err != nil {
		return fault.New(fault.StageMux, "end dinf", err)
	}
	return nil
}

func writeStbl(w *gomp4.Writer, packets []rtp.Packet, durations []uint32, opts WriteOptions, samples *[]Sample) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeStbl()}); err != nil {
		return fault.New(fault.StageMux, "start stbl", err)
	}
	if err := writeStsd(w, opts); err != nil {
		return err
	}
	if err := writeStts(w, durations); err != nil {
		return err
	}
	// One chunk per sample keeps the offsets simple and deterministic.
	if err := writeStsc(w); err != nil {
		return err
	}
	if err := writeStsz(w, packets, samples); err != nil {
		return err
	}
	if err := writeStco(w, *samples); err != nil {
		return err
	}
	if err := writeStss(w, len(packets)); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return fault.New(fault.StageMux, "end stbl", err)
	}
	return nil
}

func writeStsd(w *gomp4.Writer, opts WriteOptions) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeStsd()}); err != nil {
		return fault.New(fault.StageMux, "start stsd", err)
	}
	if _, err := gomp4.Marshal(w, &gomp4.Stsd{EntryCount: 1}, gomp4.Context{}); err != nil {
		return fault.New(fault.StageMux, "marshal stsd", err)
	}
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeAvc1()}); err != nil {
		return fault.New(fault.StageMux, "start avc1", err)
	}
	entry := &gomp4.VisualSampleEntry{
		SampleEntry:     gomp4.SampleEntry{DataReferenceIndex: 1},
		Width:           opts.Width,
		Height:          opts.Height,
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	entry.Type = gomp4.BoxTypeAvc1()
	entry.Compressorname[0] = 0
	if _, err := gomp4.Marshal(w, entry, gomp4.Context{}); err != nil {
		return fault.New(fault.StageMux, "marshal avc1", err)
	}
	if err := writeAvcC(w); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // avc1
		return fault.New(fault.StageMux, "end avc1", err)
	}
	if _, err := w.EndBox(); err != nil { // stsd
		return fault.New(fault.StageMux, "end stsd", err)
	}
	return nil
}

// writeAvcC writes a minimal decoder configuration record. The payload is not
// decoded, so the record is a placeholder with no parameter sets.
func writeAvcC(w *gomp4.Writer) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeAvcC()}); err != nil {
		return fault.New(fault.StageMux, "start avcC", err)
	}
	avcc := &gomp4.AVCDecoderConfiguration{
		ConfigurationVersion: 1,
		Profile:              gomp4.AVCBaselineProfile,
		Level:                0x1F,
		LengthSizeMinusOne:   3,
	}
	avcc.Type = gomp4.BoxTypeAvcC()
	if _, err := gomp4.Marshal(w, avcc, gomp4.Context{}); err != nil {
		return fault.New(fault.StageMux, "marshal avcC", err)
	}
	if _, err := w.EndBox(); err != nil {
		return fault.New(fault.StageMux, "end avcC", err)
	}
	return nil
}

func writeStts(w *gomp4.Writer, durations []uint32) error {
	entries := make([]gomp4.SttsEntry, 0, len(durations))
	for _, d := range durations {
		if n := len(entries); n > 0 && entries[n-1].SampleDelta == d {
			entries[n-1].SampleCount++
			continue
		}
		entries = append(entries, gomp4.SttsEntry{SampleCount: 1, SampleDelta: d})
	}
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeStts()}); err != nil {
		return fault.New(fault.StageMux, "start stts", err)
	}
	stts := &gomp4.Stts{EntryCount: uint32(len(entries)), Entries: entries}
	if _, err := gomp4.Marshal(w, stts, gomp4.Context{}); err != nil {
		return fault.New(fault.StageMux, "marshal stts", err)
	}
	if _, err := w.EndBox(); err != nil {
		return fault.New(fault.StageMux, "end stts", err)
	}
	return nil
}

func writeStsc(w *gomp4.Writer) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeStsc()}); err != nil {
		return fault.New(fault.StageMux, "start stsc", err)
	}
	stsc := &gomp4.Stsc{
		EntryCount: 1,
		Entries:    []gomp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1}},
	}
	if _, err := gomp4.Marshal(w, stsc, gomp4.Context{}); err != nil {
		return fault.New(fault.StageMux, "marshal stsc", err)
	}
	if _, err := w.EndBox(); err != nil {
		return fault.New(fault.StageMux, "end stsc", err)
	}
	return nil
}

func writeStsz(w *gomp4.Writer, packets []rtp.Packet, samples *[]Sample) error {
	sizes := make([]uint32, len(packets))
	for i, p := range packets {
		sizes[i] = uint32(len(p.Payload))
	}
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeStsz()}); err != nil {
		return fault.New(fault.StageMux, "start stsz", err)
	}
	stsz := &gomp4.Stsz{SampleCount: uint32(len(sizes)), EntrySize: sizes}
	if _, err := gomp4.Marshal(w, stsz, gomp4.Context{}); err != nil {
		return fault.New(fault.StageMux, "marshal stsz", err)
	}
	if _, err := w.EndBox(); err != nil {
		return fault.New(fault.StageMux, "end stsz", err)
	}
	// Chunk-relative offsets are filled by writeStco through *samples.
	*samples = make([]Sample, len(packets))
	var off int64
	for i, p := range packets {
		(*samples)[i] = Sample{Offset: off, Size: int64(len(p.Payload)), TS: p.TS, Dur: 0, Key: p.Marker}
		off += int64(len(p.Payload))
	}
	return nil
}

func writeStco(w *gomp4.Writer, samples []Sample) error {
	offsets := make([]uint32, len(samples))
	for i, s := range samples {
		offsets[i] = uint32(s.Offset)
	}
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeStco()}); err != nil {
		return fault.New(fault.StageMux, "start stco", err)
	}
	stco := &gomp4.Stco{EntryCount: uint32(len(offsets)), ChunkOffset: offsets}
	if _, err := gomp4.Marshal(w, stco, gomp4.Context{}); err != nil {
		return fault.New(fault.StageMux, "marshal stco", err)
	}
	if _, err := w.EndBox(); err != nil {
		return fault.New(fault.StageMux, "end stco", err)
	}
	return nil
}

// writeStss marks every sample as a sync sample: the payloads are opaque to
// this tool, so no sample is claimed to be a non-sync frame.
func writeStss(w *gomp4.Writer, n int) error {
	if n == 0 {
		return nil
	}
	nums := make([]uint32, n)
	for i := range nums {
		nums[i] = uint32(i + 1)
	}
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeStss()}); err != nil {
		return fault.New(fault.StageMux, "start stss", err)
	}
	stss := &gomp4.Stss{EntryCount: uint32(n), SampleNumber: nums}
	if _, err := gomp4.Marshal(w, stss, gomp4.Context{}); err != nil {
		return fault.New(fault.StageMux, "marshal stss", err)
	}
	if _, err := w.EndBox(); err != nil {
		return fault.New(fault.StageMux, "end stss", err)
	}
	return nil
}
