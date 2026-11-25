package mp4

import (
	"bytes"
	"io"

	gomp4 "github.com/abema/go-mp4"
)

// VideoSpec describes a synthetic video track built by BuildVideoMP4.
type VideoSpec struct {
	TrackID       uint32
	Timescale     uint32
	SampleDeltas  []uint32 // one stts entry per run: each entry is (count, delta)
	SampleCounts  []uint32
	SampleSizes   []uint32
	KeyframeIndex []uint32 // 1-based stss sample numbers
	// Damage flags used by the fixture generator.
	BadSTSSIndex    uint32 // when non-zero, append this out-of-range stss entry
	ZeroDelta       bool   // force the first stts delta to zero
	OmitSTCO        bool   // drop the chunk offset box
	OmitSTSS        bool   // drop the sync sample box
	BadChunkOffset  bool   // point every chunk offset past the end of the file
}

// BuildVideoMP4 writes a minimal but standards-shaped MP4 containing one
// video track (ftyp/moov/trak/mdia/minf/stbl plus mdat). It exists so tests
// and the fixture generator exercise the real go-mp4 reader instead of
// hand-crafted bytes.
func BuildVideoMP4(spec VideoSpec) ([]byte, error) {
	buf := &writeSeeker{buf: &bytes.Buffer{}}
	w := gomp4.NewWriter(buf)

	if err := writeFtyp(w); err != nil {
		return nil, err
	}
	if err := writeMoov(w, spec); err != nil {
		return nil, err
	}
	// mdat with one byte per sample; offsets are validated structurally.
	mdatSize := uint32(0)
	for _, n := range spec.SampleSizes {
		mdatSize += n
	}
	data := make([]byte, mdatSize)
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMdat()}); err != nil {
		return nil, err
	}
	if _, err := gomp4.Marshal(w, &gomp4.Mdat{Data: data}, gomp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	return buf.buf.Bytes(), nil
}

func writeFtyp(w *gomp4.Writer) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	ftyp := &gomp4.Ftyp{
		MajorBrand:       [4]byte{'i', 's', 'o', 'm'},
		MinorVersion:     0,
		CompatibleBrands: []gomp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}},
	}
	if _, err := gomp4.Marshal(w, ftyp, gomp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeMoov(w *gomp4.Writer, spec VideoSpec) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMoov()}); err != nil {
		return err
	}
	if err := writeTrak(w, spec); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeTrak(w *gomp4.Writer, spec VideoSpec) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if err := box(w, gomp4.BoxTypeTkhd(), &gomp4.Tkhd{
		TrackID:    spec.TrackID,
		DurationV0: totalDuration(spec),
		Width:      320 << 16,
		Height:     240 << 16,
	}); err != nil {
		return err
	}
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if err := box(w, gomp4.BoxTypeMdhd(), &gomp4.Mdhd{
		Timescale:  spec.Timescale,
		DurationV0: totalDuration(spec),
	}); err != nil {
		return err
	}
	if err := box(w, gomp4.BoxTypeHdlr(), &gomp4.Hdlr{
		HandlerType: [4]byte{'v', 'i', 'd', 'e'},
		Name:        "VideoHandler\x00",
	}); err != nil {
		return err
	}
	if err := writeMinf(w, spec); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return err
	}
	_, err := w.EndBox() // trak
	return err
}

func writeMinf(w *gomp4.Writer, spec VideoSpec) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if err := writeSTBL(w, spec); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // stbl
		return err
	}
	_, err := w.EndBox() // minf
	return err
}

func writeSTBL(w *gomp4.Writer, spec VideoSpec) error {
	entries := make([]gomp4.SttsEntry, len(spec.SampleCounts))
	for i := range entries {
		delta := spec.SampleDeltas[i]
		if spec.ZeroDelta && i == 0 {
			delta = 0
		}
		entries[i] = gomp4.SttsEntry{SampleCount: spec.SampleCounts[i], SampleDelta: delta}
	}
	if err := box(w, gomp4.BoxTypeStts(), &gomp4.Stts{
		EntryCount: uint32(len(entries)),
		Entries:    entries,
	}); err != nil {
		return err
	}
	if !spec.OmitSTSS {
		indexes := append([]uint32(nil), spec.KeyframeIndex...)
		if spec.BadSTSSIndex != 0 {
			indexes = append(indexes, spec.BadSTSSIndex)
		}
		if err := box(w, gomp4.BoxTypeStss(), &gomp4.Stss{
			EntryCount:   uint32(len(indexes)),
			SampleNumber: indexes,
		}); err != nil {
			return err
		}
	}
	// One sample per chunk.
	stscEntries := []gomp4.StscEntry{{
		FirstChunk:             1,
		SamplesPerChunk:        1,
		SampleDescriptionIndex: 1,
	}}
	if err := box(w, gomp4.BoxTypeStsc(), &gomp4.Stsc{
		EntryCount: 1,
		Entries:    stscEntries,
	}); err != nil {
		return err
	}
	if err := box(w, gomp4.BoxTypeStsz(), &gomp4.Stsz{
		SampleSize:  0,
		SampleCount: uint32(len(spec.SampleSizes)),
		EntrySize:   spec.SampleSizes,
	}); err != nil {
		return err
	}
	if !spec.OmitSTCO {
		// Healthy offsets sit inside the generated file's mdat payload.
		// The damage flag instead points every chunk beyond the file end.
		offsets := make([]uint32, len(spec.SampleSizes))
		base := uint32(64)
		stride := uint32(4)
		if spec.BadChunkOffset {
			base = 0x70000000
		}
		for i := range offsets {
			offsets[i] = base + uint32(i)*stride
		}
		if err := box(w, gomp4.BoxTypeStco(), &gomp4.Stco{
			EntryCount:  uint32(len(offsets)),
			ChunkOffset: offsets,
		}); err != nil {
			return err
		}
	}
	return nil
}

func box(w *gomp4.Writer, typ gomp4.BoxType, payload gomp4.IImmutableBox) error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: typ}); err != nil {
		return err
	}
	if _, err := gomp4.Marshal(w, payload, gomp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func totalDuration(spec VideoSpec) uint32 {
	var total uint32
	for i, count := range spec.SampleCounts {
		total += count * spec.SampleDeltas[i]
	}
	return total
}

// writeSeeker adapts a bytes.Buffer to io.WriteSeeker for go-mp4.
type writeSeeker struct {
	buf *bytes.Buffer
	pos int64
}

func (w *writeSeeker) Write(p []byte) (int, error) {
	end := w.pos + int64(len(p))
	if end > int64(w.buf.Len()) {
		grow := make([]byte, end-int64(w.buf.Len()))
		if _, err := w.buf.Write(grow); err != nil {
			return 0, err
		}
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
