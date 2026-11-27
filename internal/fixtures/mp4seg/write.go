// Package mp4seg writes and reads the project's MP4 clips using go-mp4.
//
// Layout is deterministic: ftyp, moov (one AV1 video track), mdat. Each sample
// is an independently decodable still, and sample timestamps are expressed as
// stts deltas, so the verifier can recover timing from the file alone.
package mp4seg

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	mp4 "github.com/abema/go-mp4"
)

// Sample is one encoded still plus its presentation time in TimeScale ticks.
// Render uses a nanosecond timescale (1e9) so stts round-trips the shared
// nanosecond base without rounding.
type Sample struct {
	Data  []byte
	Ticks int64
}

// Segment is the in-memory form of a rendered clip.
type Segment struct {
	TimeScale uint32 // media timescale in ticks per second
	Width     int
	Height    int
	Samples   []Sample
}

// TrackMeta is what a reader recovers from a segment without decoding pixels.
type TrackMeta struct {
	TrackID     uint32
	TimeScale   uint32
	Width       int
	Height      int
	SampleSizes []uint32
	ChunkOffset []uint32
	Deltas      []uint32
	Codec       string
}

// av1ConfigOBUs is a minimal av1C config OBU: OBU_SEQUENCE_HEADER with a
// 11-byte payload declaring profile 0, 8-bit 4:2:0, level 4.0. The renderer
// emits still frames, so no temporal layers or parameter-set updates exist.
var av1ConfigOBUs = []byte{
	0x0a, 0x0b,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
}

// Write serialises a segment. Output is byte-stable for equal input.
func Write(w io.Writer, seg Segment) error {
	if seg.Width <= 0 || seg.Height <= 0 {
		return fmt.Errorf("mp4seg: empty geometry")
	}
	if len(seg.Samples) == 0 {
		return fmt.Errorf("mp4seg: segment has no samples")
	}
	if seg.TimeScale == 0 {
		return fmt.Errorf("mp4seg: zero timescale")
	}
	sizes, deltas, err := sampleTables(seg)
	if err != nil {
		return err
	}

	ftypBytes, err := marshalBox(fileTypeBox())
	if err != nil {
		return err
	}
	// Two passes: the second sees the true mdat data offsets.
	offsets := make([]uint32, len(seg.Samples))
	moov, err := buildMoov(seg, sizes, deltas, offsets)
	if err != nil {
		return err
	}
	mdatHeaderSize := 8
	var payload uint64
	for _, s := range seg.Samples {
		payload += uint64(len(s.Data))
	}
	if payload > uint64(^uint32(0))-16 {
		mdatHeaderSize = 16
	}
	next := uint64(len(ftypBytes)+8) + uint64(len(moov)) + uint64(mdatHeaderSize)
	for i, s := range seg.Samples {
		if next > uint64(^uint32(0)) {
			return fmt.Errorf("mp4seg: sample offsets exceed 32-bit stco")
		}
		offsets[i] = uint32(next)
		next += uint64(len(s.Data))
	}
	moov, err = buildMoov(seg, sizes, deltas, offsets)
	if err != nil {
		return err
	}

	var out bytes.Buffer
	if err := writeBox(&out, mp4.BoxTypeFtyp(), ftypBytes); err != nil {
		return err
	}
	if err := writeBox(&out, mp4.BoxTypeMoov(), moov); err != nil {
		return err
	}
	if err := writeMdat(&out, seg.Samples, payload); err != nil {
		return err
	}
	_, err = w.Write(out.Bytes())
	return err
}

// sampleTables derives stsz entry sizes and stts deltas. Consecutive equal
// deltas are collapsed by buildMoov; the raw deltas are kept per sample so the
// reader can validate timing exactly.
func sampleTables(seg Segment) ([]uint32, []uint32, error) {
	sizes := make([]uint32, len(seg.Samples))
	deltas := make([]uint32, len(seg.Samples))
	for i, s := range seg.Samples {
		if len(s.Data) == 0 {
			return nil, nil, fmt.Errorf("mp4seg: sample %d is empty", i)
		}
		sizes[i] = uint32(len(s.Data))
		if i == 0 {
			continue
		}
		d := seg.Samples[i].Ticks - seg.Samples[i-1].Ticks
		if d <= 0 {
			return nil, nil, fmt.Errorf("mp4seg: sample %d timestamp is not increasing", i)
		}
		deltas[i] = uint32(d)
	}
	if len(seg.Samples) > 1 {
		deltas[0] = deltas[1]
	} else {
		deltas[0] = seg.TimeScale / 30
	}
	return sizes, deltas, nil
}

func fileTypeBox() *mp4.Ftyp {
	f := &mp4.Ftyp{MajorBrand: [4]byte{'i', 's', 'o', '6'}}
	f.AddCompatibleBrand([4]byte{'i', 's', 'o', '6'})
	f.AddCompatibleBrand([4]byte{'a', 'v', '0', '1'})
	f.AddCompatibleBrand([4]byte{'i', 's', 'o', 'm'})
	return f
}

func buildMoov(seg Segment, sizes, deltas, offsets []uint32) ([]byte, error) {
	var dur uint64
	for _, d := range deltas {
		dur += uint64(d)
	}
	var moov bytes.Buffer
	if err := writeMvhd(&moov, seg.TimeScale, dur, 2); err != nil {
		return nil, err
	}
	if err := writeTrak(&moov, seg, sizes, deltas, offsets, dur); err != nil {
		return nil, err
	}
	return moov.Bytes(), nil
}

func writeMvhd(w io.Writer, timescale uint32, duration uint64, nextTrackID uint32) error {
	mvhd := &mp4.Mvhd{
		Timescale:   timescale,
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      identityMatrix(),
		NextTrackID: nextTrackID,
	}
	setDuration(mvhd, duration)
	body, err := marshalBox(mvhd)
	if err != nil {
		return err
	}
	return writeBox(w, mp4.BoxTypeMvhd(), body)
}

func identityMatrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}

func writeTrak(w io.Writer, seg Segment, sizes, deltas, offsets []uint32, dur uint64) error {
	var trak bytes.Buffer

	tkhd := &mp4.Tkhd{
		TrackID: 1,
		Matrix:  identityMatrix(),
		Width:   uint32(seg.Width) << 16,
		Height:  uint32(seg.Height) << 16,
	}
	// FullBox version 0 duration plus the enabled|in-movie flags.
	if dur > uint64(^uint32(0)) {
		tkhd.SetVersion(1)
		tkhd.DurationV1 = dur
	} else {
		tkhd.DurationV0 = uint32(dur)
	}
	tkhd.SetFlags(0x000003)
	tkhdBody, err := marshalBox(tkhd)
	if err != nil {
		return err
	}
	if err := writeBox(&trak, mp4.BoxTypeTkhd(), tkhdBody); err != nil {
		return err
	}

	var mdia bytes.Buffer
	mdhd := &mp4.Mdhd{Timescale: seg.TimeScale, Language: [3]byte{'u', 'n', 'd'}}
	setMdhdDuration(mdhd, dur)
	mdhdBody, err := marshalBox(mdhd)
	if err != nil {
		return err
	}
	if err := writeBox(&mdia, mp4.BoxTypeMdhd(), mdhdBody); err != nil {
		return err
	}
	hdlr := &mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "medialab"}
	hdlrBody, err := marshalBox(hdlr)
	if err != nil {
		return err
	}
	if err := writeBox(&mdia, mp4.BoxTypeHdlr(), hdlrBody); err != nil {
		return err
	}

	var minf bytes.Buffer
	vmhdBody, err := marshalBox(&mp4.Vmhd{})
	if err != nil {
		return err
	}
	if err := writeBox(&minf, mp4.BoxTypeVmhd(), vmhdBody); err != nil {
		return err
	}
	dinf, err := buildDinf()
	if err != nil {
		return err
	}
	if err := writeBox(&minf, mp4.BoxTypeDinf(), dinf); err != nil {
		return err
	}
	stbl, err := buildStbl(seg, sizes, deltas, offsets)
	if err != nil {
		return err
	}
	if err := writeBox(&minf, mp4.BoxTypeStbl(), stbl); err != nil {
		return err
	}
	if err := writeBox(&mdia, mp4.BoxTypeMinf(), minf.Bytes()); err != nil {
		return err
	}
	if err := writeBox(&trak, mp4.BoxTypeMdia(), mdia.Bytes()); err != nil {
		return err
	}
	return writeBox(w, mp4.BoxTypeTrak(), trak.Bytes())
}

func buildDinf() ([]byte, error) {
	var dref bytes.Buffer
	// dref is a full box: version+flags, entry_count, then url .
	dref.Write([]byte{0, 0, 0, 0, 0, 0, 0, 1})
	urlBody, err := marshalBox(&mp4.Url{})
	if err != nil {
		return nil, err
	}
	// url with the self-contained flag set (0x000001) carries no location.
	var urlBox bytes.Buffer
	if err := writeBox(&urlBox, mp4.BoxTypeUrl(), urlBody); err != nil {
		return nil, err
	}
	// Patch the self-contained flag in the version+flags field.
	b := urlBox.Bytes()
	if len(b) < 12 {
		return nil, fmt.Errorf("mp4seg: short url box")
	}
	b[11] = 0x01
	dref.Write(b)

	var dinf bytes.Buffer
	if err := writeBox(&dinf, mp4.BoxTypeDref(), dref.Bytes()); err != nil {
		return nil, err
	}
	return dinf.Bytes(), nil
}

func buildStbl(seg Segment, sizes, deltas, offsets []uint32) ([]byte, error) {
	var stbl bytes.Buffer

	stsd, err := buildStsd(seg)
	if err != nil {
		return nil, err
	}
	if err := writeBox(&stbl, mp4.BoxTypeStsd(), stsd); err != nil {
		return nil, err
	}

	sttsBody, err := marshalBox(buildStts(deltas))
	if err != nil {
		return nil, err
	}
	if err := writeBox(&stbl, mp4.BoxTypeStts(), sttsBody); err != nil {
		return nil, err
	}

	stsc := &mp4.Stsc{
		EntryCount: 1,
		Entries: []mp4.StscEntry{{
			FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1,
		}},
	}
	stscBody, err := marshalBox(stsc)
	if err != nil {
		return nil, err
	}
	if err := writeBox(&stbl, mp4.BoxTypeStsc(), stscBody); err != nil {
		return nil, err
	}

	stsz := &mp4.Stsz{SampleCount: uint32(len(sizes)), EntrySize: sizes}
	stszBody, err := marshalBox(stsz)
	if err != nil {
		return nil, err
	}
	if err := writeBox(&stbl, mp4.BoxTypeStsz(), stszBody); err != nil {
		return nil, err
	}

	if len(offsets) > 0 {
		stco := &mp4.Stco{EntryCount: uint32(len(offsets)), ChunkOffset: offsets}
		stcoBody, err := marshalBox(stco)
		if err != nil {
			return nil, err
		}
		if err := writeBox(&stbl, mp4.BoxTypeStco(), stcoBody); err != nil {
			return nil, err
		}
	}
	return stbl.Bytes(), nil
}

func buildStts(deltas []uint32) *mp4.Stts {
	entries := make([]mp4.SttsEntry, 0, len(deltas))
	for _, d := range deltas {
		if n := len(entries); n > 0 && entries[n-1].SampleDelta == d {
			entries[n-1].SampleCount++
			continue
		}
		entries = append(entries, mp4.SttsEntry{SampleCount: 1, SampleDelta: d})
	}
	return &mp4.Stts{EntryCount: uint32(len(entries)), Entries: entries}
}

func buildStsd(seg Segment) ([]byte, error) {
	var body bytes.Buffer
	body.Write([]byte{0, 0, 0, 0}) // version + flags
	writeU32(&body, 1)             // entry count

	entry := &mp4.VisualSampleEntry{
		PreDefined:      0,
		Width:           uint16(seg.Width),
		Height:          uint16(seg.Height),
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
		PreDefined3:     -1,
	}
	entry.DataReferenceIndex = 1
	entry.Type = mp4.BoxTypeAv01()
	entryBody, err := marshalVisualEntry(entry)
	if err != nil {
		return nil, err
	}

	var av1c bytes.Buffer
	if err := writeBox(&av1c, mp4.BoxTypeAv1C(), av1ConfigBody()); err != nil {
		return nil, err
	}
	entryBody = append(entryBody, av1c.Bytes()...)
	if err := writeBox(&body, mp4.BoxTypeAv01(), entryBody); err != nil {
		return nil, err
	}
	return body.Bytes(), nil
}

// av1ConfigBody marshals the Av1C payload without its own box header.
func av1ConfigBody() []byte {
	av1c := &mp4.Av1C{
		SeqProfile:         0,
		SeqLevelIdx0:       8,
		ChromaSubsamplingX: 1,
		ChromaSubsamplingY: 1,
		ConfigOBUs:         av1ConfigOBUs,
	}
	body, err := marshalBox(av1c)
	if err != nil {
		// The struct is static; a failure here is a programmer error.
		panic(fmt.Sprintf("mp4seg: marshal av1C: %v", err))
	}
	return body
}

var mdatType = [4]byte{'m', 'd', 'a', 't'}

func writeMdat(w io.Writer, samples []Sample, payload uint64) error {
	if payload <= uint64(^uint32(0))-16 {
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[0:4], uint32(payload)+8)
		copy(hdr[4:8], mdatType[:])
		if _, err := w.Write(hdr[:]); err != nil {
			return err
		}
	} else {
		var hdr [16]byte
		binary.BigEndian.PutUint32(hdr[0:4], 1)
		copy(hdr[4:8], mdatType[:])
		binary.BigEndian.PutUint64(hdr[8:16], payload+16)
		if _, err := w.Write(hdr[:]); err != nil {
			return err
		}
	}
	for _, s := range samples {
		if _, err := w.Write(s.Data); err != nil {
			return err
		}
	}
	return nil
}

func writeU32(w io.Writer, v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	_, _ = w.Write(b[:])
}

// setDuration stores a duration, switching to the version-1 64-bit field when
// the value does not fit in 32 bits.
func setDuration(b *mp4.Mvhd, duration uint64) {
	if duration > uint64(^uint32(0)) {
		b.SetVersion(1)
		b.DurationV1 = duration
		return
	}
	b.DurationV0 = uint32(duration)
}

func setMdhdDuration(b *mp4.Mdhd, duration uint64) {
	if duration > uint64(^uint32(0)) {
		b.SetVersion(1)
		b.DurationV1 = duration
		return
	}
	b.DurationV0 = uint32(duration)
}

// marshalVisualEntry marshals a VisualSampleEntry. The type-specific box
// (av01) is registered via AnyTypeBox, so Marshal resolves it through
// entry.Type; we wrap it here to keep the call sites readable.
func marshalVisualEntry(e *mp4.VisualSampleEntry) ([]byte, error) {
	return marshalBox(e)
}

func marshalBox(b mp4.IImmutableBox) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := mp4.Marshal(&buf, b, mp4.Context{}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeBox(w io.Writer, typ mp4.BoxType, payload []byte) error {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(len(payload)+8))
	copy(hdr[4:8], typ[:])
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}
