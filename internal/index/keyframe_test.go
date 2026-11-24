package index

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/abema/go-mp4"

	"mediarun/internal/fault"
)

// writeMinimalMP4 builds a tiny file with a one-track stbl whose sample data
// lives in a single chunk after moov.
func writeMinimalMP4(t *testing.T, dir string, samples [][]byte) (string, []int64) {
	t.Helper()
	path := filepath.Join(dir, "mini.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := mp4.NewWriter(f)

	// ftyp
	if _, err := mp4.Marshal(w, &mp4.Ftyp{MajorBrand: mp4.BrandISOM()}, mp4.Context{}); err != nil {
		t.Fatal(err)
	}
	// We need box framing; Marshal above wrote only the payload. Use a manual
	// BoxInfo approach for clarity.
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(0); err != nil {
		t.Fatal(err)
	}
	w = mp4.NewWriter(f)
	writeTestBox(t, w, &mp4.Ftyp{MajorBrand: mp4.BrandISOM()})
	writeTestBox(t, w, &mp4.Free{Box: mp4.Box{}})

	moov, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()})
	if err != nil {
		t.Fatal(err)
	}
	trak, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()})
	if err != nil {
		t.Fatal(err)
	}
	tkhd := &mp4.Tkhd{TrackID: 1, Matrix: [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}}
	if err := writeTestBox(t, w, tkhd); err != nil {
		t.Fatal(err)
	}
	mdia, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()})
	if err != nil {
		t.Fatal(err)
	}
	writeTestBox(t, w, &mp4.Mdhd{Timescale: 90000, DurationV0: 100})
	writeTestBox(t, w, &mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}})
	minf, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()})
	if err != nil {
		t.Fatal(err)
	}
	vmhd := &mp4.Vmhd{}
	vmhd.SetFlags(1)
	writeTestBox(t, w, vmhd)
	writeTestBox(t, w, &mp4.Dinf{})
	stbl, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()})
	if err != nil {
		t.Fatal(err)
	}
	writeTestBox(t, w, &mp4.Stts{EntryCount: 1, Entries: []mp4.SttsEntry{{SampleCount: uint32(len(samples)), SampleDelta: 3000}}})
	ss := &mp4.Stss{}
	for i := range samples {
		ss.SampleNumber = append(ss.SampleNumber, uint32(i+1))
	}
	ss.EntryCount = uint32(len(ss.SampleNumber))
	writeTestBox(t, w, ss)
	writeTestBox(t, w, &mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: uint32(len(samples)), SampleDescriptionIndex: 1}}})
	sz := &mp4.Stsz{SampleCount: uint32(len(samples))}
	for _, s := range samples {
		sz.EntrySize = append(sz.EntrySize, uint32(len(s)))
	}
	writeTestBox(t, w, sz)
	// stco placeholder; patched after mdat offset is known.
	stco := &mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{0}}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStco()}); err != nil {
		t.Fatal(err)
	}
	stcoStart, _ := w.Seek(0, 1)
	if _, err := mp4.Marshal(w, stco, mp4.Context{}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.EndBox(); err != nil {
		t.Fatal(err)
	}
	w.EndBox() // stbl
	w.EndBox() // minf
	w.EndBox() // mdia
	w.EndBox() // trak
	w.EndBox() // moov
	_ = stbl
	_ = mdia
	_ = minf
	_ = trak
	_ = moov

	mdat, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()})
	if err != nil {
		t.Fatal(err)
	}
	chunkStart, _ := w.Seek(0, 1)
	var offs []int64
	for _, s := range samples {
		offs = append(offs, chunkStart)
		if _, err := w.Write(s); err != nil {
			t.Fatal(err)
		}
		chunkStart += int64(len(s))
	}
	if _, err := w.EndBox(); err != nil {
		t.Fatal(err)
	}
	// Patch stco chunk offset: version/flags (4) + entry count (4).
	payload := stcoStart + 8
	if _, err := w.Seek(payload, 0); err != nil {
		t.Fatal(err)
	}
	var raw [4]byte
	v := uint32(mdat.Offset + mdat.HeaderSize)
	raw[0], raw[1], raw[2], raw[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
	if _, err := w.Write(raw[:]); err != nil {
		t.Fatal(err)
	}
	return path, offs
}

func writeTestBox(t *testing.T, w *mp4.Writer, box mp4.IImmutableBox) error {
	t.Helper()
	if _, err := w.StartBox(&mp4.BoxInfo{Type: box.GetType()}); err != nil {
		t.Fatal(err)
	}
	if _, err := mp4.Marshal(w, box, mp4.Context{}); err != nil {
		t.Fatal(err)
	}
	_, err := w.EndBox()
	if err != nil {
		t.Fatal(err)
	}
	return nil
}

func TestReadTableAndOffsets(t *testing.T) {
	dir := t.TempDir()
	payloads := [][]byte{[]byte("aaaa"), []byte("bbbbbb"), []byte("cc")}
	path, woffs := writeMinimalMP4(t, dir, payloads)
	tbl, err := ReadTable(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl.SampleSizes) != 3 || len(tbl.ChunkOffset) != 1 {
		t.Fatalf("table = %+v", tbl)
	}
	offs, err := SampleOffsets(tbl)
	if err != nil {
		t.Fatal(err)
	}
	base := int64(tbl.ChunkOffset[0])
	for i, off := range offs {
		if off != base+woffs[i]-woffs[0] {
			t.Fatalf("offset %d = %d want %d", i, off, base+woffs[i]-woffs[0])
		}
	}
}

func TestVerifyDetectsShiftedIndex(t *testing.T) {
	dir := t.TempDir()
	payloads := [][]byte{[]byte("aaaa"), []byte("bbbbbb"), []byte("cc")}
	path, woffs := writeMinimalMP4(t, dir, payloads)
	tbl, err := ReadTable(path)
	if err != nil {
		t.Fatal(err)
	}
	offs, _ := SampleOffsets(tbl)
	_ = woffs
	idx := &Index{
		VideoTrack:  1,
		SampleCount: 3,
		Entries: []Entry{
			{Sample: 1, Track: 1, DataOffset: offs[0], Size: 4, PayloadHash: PayloadHash(payloads[0])},
			{Sample: 2, Track: 1, DataOffset: offs[1], Size: 6, PayloadHash: PayloadHash(payloads[1])},
		},
	}
	res, err := Verify(path, idx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Verified() || res.Ratio() != 100 {
		t.Fatalf("verify = %+v", res)
	}
	idx.Entries[0].DataOffset++
	res, err = Verify(path, idx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verified() {
		t.Fatal("shifted offset verified")
	}
	if len(res.Misses) != 1 {
		t.Fatalf("misses = %v", res.Misses)
	}
}

func TestVerifyRejectsEmptyAndBadCount(t *testing.T) {
	dir := t.TempDir()
	path, _ := writeMinimalMP4(t, dir, [][]byte{[]byte("a")})
	if _, err := Verify(path, nil); err == nil {
		t.Fatal("nil index accepted")
	}
	// An index with no keyframe entries is legal but resolves nothing.
	empty, err := Verify(path, &Index{SampleCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Verified() || empty.Ratio() != 0 {
		t.Fatalf("empty entry list reported %+v", empty)
	}
	bad := &Index{SampleCount: 99, Entries: []Entry{{Sample: 1}}}
	_, err = Verify(path, bad)
	if err == nil {
		t.Fatal("sample count mismatch accepted")
	}
	if fe, ok := fault.As(err); !ok || fe.Kind != fault.KindIndex {
		t.Fatalf("wrong fault: %v", err)
	}
}

func TestSampleOffsetsRejectsMissingTables(t *testing.T) {
	if _, err := SampleOffsets(&Table{}); err == nil {
		t.Fatal("empty table accepted")
	}
	if _, err := SampleOffsets(&Table{ChunkOffset: []uint32{1}}); err == nil {
		t.Fatal("missing stsc accepted")
	}
}

func TestIndexSortAndCount(t *testing.T) {
	x := &Index{Entries: []Entry{{Sample: 3}, {Sample: 1}, {Sample: 2}}}
	x.Sort()
	if x.Count() != 3 || x.Entries[0].Sample != 1 {
		t.Fatalf("sort = %+v", x.Entries)
	}
	var empty VerifyResult
	if empty.Ratio() != 0 || empty.Verified() {
		t.Fatal("empty result should report zero")
	}
	a, b := PayloadHash([]byte("x")), PayloadHash([]byte("x"))
	if !bytes.Equal(a[:], b[:]) {
		t.Fatal("digest unstable")
	}
}
