package fixture

import (
	"os"

	"github.com/abema/go-mp4"
)

// WriteMultiChunkMP4 builds an MP4 whose samples are spread across several
// chunks with a multi-entry stsc table, exercising the parser's run-length
// sample-to-chunk expansion rather than the single-chunk shortcut.
func WriteMultiChunkMP4(path string, timescale uint32, payload [][]byte) error {
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := mp4.NewWriter(f)

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand:       [4]byte{'i', 's', 'o', 'm'},
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}},
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Mdhd{
		Timescale:  timescale,
		DurationV0: uint32(len(payload)) * 1000,
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // mdhd
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}

	stts := make([]mp4.SttsEntry, len(payload))
	for i := range stts {
		stts[i] = mp4.SttsEntry{SampleCount: 1, SampleDelta: 1000}
	}
	if err := marshalBox(w, mp4.BoxTypeStts(), &mp4.Stts{
		EntryCount: uint32(len(stts)), Entries: stts,
	}); err != nil {
		return err
	}
	// Two chunks: chunk 1 holds the first two samples, chunk 2 the rest.
	// stsc entries switch the samples-per-chunk at chunk 2.
	if err := marshalBox(w, mp4.BoxTypeStsc(), &mp4.Stsc{
		EntryCount: 2,
		Entries: []mp4.StscEntry{
			{FirstChunk: 1, SamplesPerChunk: 2},
			{FirstChunk: 2, SamplesPerChunk: uint32(len(payload) - 2)},
		},
	}); err != nil {
		return err
	}
	sizes := make([]uint32, len(payload))
	for i, p := range payload {
		sizes[i] = uint32(len(p))
	}
	if err := marshalBox(w, mp4.BoxTypeStsz(), &mp4.Stsz{
		SampleCount: uint32(len(sizes)), EntrySize: sizes,
	}); err != nil {
		return err
	}
	// Two placeholder chunk offsets, patched after the mdat is written.
	if err := marshalBox(w, mp4.BoxTypeStco(), &mp4.Stco{
		EntryCount:  2,
		ChunkOffset: []uint32{0, 0},
	}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // stbl
		return err
	}
	if _, err := w.EndBox(); err != nil { // minf
		return err
	}
	if _, err := w.EndBox(); err != nil { // trak
		return err
	}
	if _, err := w.EndBox(); err != nil { // moov
		return err
	}

	// Emit two separate mdat boxes, one per chunk.
	var chunkStarts []uint64
	for _, chunk := range [][][]byte{payload[:2], payload[2:]} {
		bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()})
		if err != nil {
			return err
		}
		chunkStarts = append(chunkStarts, bi.Offset+bi.HeaderSize)
		for _, p := range chunk {
			if _, err := w.Write(p); err != nil {
				return err
			}
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
	}
	return patchTwoChunkOffsets(f, chunkStarts)
}

func marshalBox(w *mp4.Writer, typ mp4.BoxType, box mp4.IImmutableBox) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, box, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

// patchTwoChunkOffsets rewrites both placeholder offsets in the stco box.
func patchTwoChunkOffsets(f *os.File, starts []uint64) error {
	var stcoAbs int64 = -1
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		if h.Path[len(h.Path)-1] == mp4.BoxTypeStco() {
			stcoAbs = int64(h.BoxInfo.Offset + h.BoxInfo.HeaderSize + 8)
		}
		return h.Expand()
	}
	if _, err := mp4.ReadBoxStructure(f, handler); err != nil {
		return err
	}
	if stcoAbs < 0 {
		return os.ErrInvalid
	}
	for i, start := range starts {
		var buf [4]byte
		buf[0] = byte(start >> 24)
		buf[1] = byte(start >> 16)
		buf[2] = byte(start >> 8)
		buf[3] = byte(start)
		if _, err := f.WriteAt(buf[:], stcoAbs+int64(i)*4); err != nil {
			return err
		}
	}
	return nil
}
