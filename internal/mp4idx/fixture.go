package mp4idx

import (
	"errors"
	"io"

	"github.com/abema/go-mp4"
)

// FixtureSpec describes a minimal single-track MP4 built for tests.
type FixtureSpec struct {
	Timescale       uint32
	SampleSizes     []uint32
	SampleDeltas    []uint32 // one delta per sample (constant delta typical)
	SyncSamples     []uint32 // 1-based; nil writes no stss (all sync)
	ChunkOffsetBias int64    // added to the natural mdat data offset
	CorruptStss     bool     // write an stss entry past the sample range
}

// BuildFixture serializes a minimal ftyp/moov/mdat MP4 into memory.
func BuildFixture(spec FixtureSpec) ([]byte, error) {
	mem := &seekBuffer{}
	w := mp4.NewWriter(mem)
	n := len(spec.SampleSizes)

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return nil, err
	}
	if err := marshalBox(w, &mp4.Ftyp{MajorBrand: [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0, CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}}}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	// Reserve mdat first by writing a placeholder, then moov references it.
	mdatStart, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()})
	if err != nil {
		return nil, err
	}
	dataOffset := int64(mdatStart.Offset+mdatStart.HeaderSize) + spec.ChunkOffsetBias
	payload := make([]byte, sumSizes(spec.SampleSizes))
	if _, err := w.Write(payload); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return nil, err
	}
	if err := writeMovie(w, spec, n, uint64(dataOffset)); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	return mem.Bytes(), nil
}

func writeMovie(w *mp4.Writer, spec FixtureSpec, n int, dataOffset uint64) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	if err := marshalBox(w, &mp4.Mdhd{Timescale: spec.Timescale, DurationV0: uint32(n * 3000)}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if err := writeStbl(w, spec, n, dataOffset); err != nil {
		return err
	}
	for i := 0; i < 3; i++ { // stbl, minf, mdia
		if _, err := w.EndBox(); err != nil {
			return err
		}
	}
	if _, err := w.EndBox(); err != nil { // trak
		return err
	}
	return nil
}

func writeStbl(w *mp4.Writer, spec FixtureSpec, n int, dataOffset uint64) error {
	// stts: run-length encode the per-sample deltas.
	entries := rleDeltas(spec.SampleDeltas)
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStts()}); err != nil {
		return err
	}
	if err := marshalBox(w, &mp4.Stts{EntryCount: uint32(len(entries)), Entries: entries}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	// stsc: every chunk holds one sample.
	sc := make([]mp4.StscEntry, n)
	for i := range sc {
		sc[i] = mp4.StscEntry{FirstChunk: uint32(i + 1), SamplesPerChunk: 1, SampleDescriptionIndex: 1}
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsc()}); err != nil {
		return err
	}
	if err := marshalBox(w, &mp4.Stsc{EntryCount: uint32(n), Entries: sc}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	// stsz
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsz()}); err != nil {
		return err
	}
	if err := marshalBox(w, &mp4.Stsz{SampleSize: 0, SampleCount: uint32(n), EntrySize: spec.SampleSizes}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	// stco: one chunk per sample, offsets walked by sample size.
	offsets := make([]uint32, n)
	cur := dataOffset
	for i, sz := range spec.SampleSizes {
		offsets[i] = uint32(cur)
		cur += uint64(sz)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStco()}); err != nil {
		return err
	}
	if err := marshalBox(w, &mp4.Stco{EntryCount: uint32(n), ChunkOffset: offsets}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	// stss (optional)
	sync := spec.SyncSamples
	if spec.CorruptStss {
		sync = []uint32{uint32(n) + 5}
	}
	if sync != nil || spec.CorruptStss {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStss()}); err != nil {
			return err
		}
		if err := marshalBox(w, &mp4.Stss{EntryCount: uint32(len(sync)), SampleNumber: sync}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
	}
	return nil
}

func marshalBox(w io.Writer, box mp4.IImmutableBox) error {
	_, err := mp4.Marshal(w, box, mp4.Context{})
	return err
}

func rleDeltas(deltas []uint32) []mp4.SttsEntry {
	if len(deltas) == 0 {
		return nil
	}
	var out []mp4.SttsEntry
	for _, d := range deltas {
		if len(out) > 0 && out[len(out)-1].SampleDelta == d {
			out[len(out)-1].SampleCount++
		} else {
			out = append(out, mp4.SttsEntry{SampleCount: 1, SampleDelta: d})
		}
	}
	return out
}

func sumSizes(s []uint32) int {
	t := 0
	for _, v := range s {
		t += int(v)
	}
	return t
}

var errNegativeSeek = errors.New("negative seek position")

// seekBuffer is an in-memory io.WriteSeeker that supports backwards seeks so
// that mp4.Writer can patch box size headers.
type seekBuffer struct {
	data []byte
	pos  int64
}

func (s *seekBuffer) Write(p []byte) (int, error) {
	end := s.pos + int64(len(p))
	if end > int64(len(s.data)) {
		grown := make([]byte, end)
		copy(grown, s.data)
		s.data = grown
	}
	copy(s.data[s.pos:end], p)
	s.pos = end
	return len(p), nil
}

func (s *seekBuffer) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		s.pos = offset
	case io.SeekCurrent:
		s.pos += offset
	case io.SeekEnd:
		s.pos = int64(len(s.data)) + offset
	}
	if s.pos < 0 {
		return 0, errNegativeSeek
	}
	return s.pos, nil
}

// Bytes returns the serialized contents.
func (s *seekBuffer) Bytes() []byte { return s.data }
