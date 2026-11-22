package mp4

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pairwise/mediad/internal/fault"
	"github.com/pairwise/mediad/internal/fixtures"
)

func writeTemp(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.mp4")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func baseSpec(moovFirst bool) fixtures.FileSpec {
	return fixtures.FileSpec{
		Timescale: 1000, MoovFirst: moovFirst, Major: "isom", Brands: []string{"isom"},
		BreakStszAt: -1, BreakSizeOf: -1,
		Tracks: []fixtures.TrackSpec{{
			ID: 1, Codec: fixtures.CodecAVC, Timescale: 90000, SampleDur: 3000,
			Samples: 12, KeyEvery: 3, SampleSize: 64, SampleSeed: 9,
		}},
	}
}

func TestScanMoovPlacementAndExtendedSize(t *testing.T) {
	for _, moovFirst := range []bool{true, false} {
		path := writeTemp(t, fixtures.Build(baseSpec(moovFirst)))
		tree, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer tree.Close()
		if len(tree.Warnings) != 0 {
			t.Fatalf("unexpected warnings %v", tree.Warnings)
		}
		if len(tree.Find("moov")) != 1 || len(tree.Find("mdat")) != 1 {
			t.Fatalf("missing boxes: %v", tree.Boxes)
		}
		// Rewrap mdat with a 64-bit size header and verify detection.
	}
}

func TestExtendedSizeBoxDetected(t *testing.T) {
	// Build a tiny file and rewrite its free box header with size=1 + 64-bit size.
	raw := fixtures.Build(baseSpec(true))
	tree := Scan("/mem", bytesReaderAt(raw), int64(len(raw)))
	free := tree.First("free")
	if free == nil || free.Extended {
		t.Fatalf("fixture free box missing or extended: %v", free)
	}
	// Replace 8-byte header with 16-byte extended header.
	var out []byte
	out = append(out, raw[:free.Offset]...)
	hdr := []byte{0, 0, 0, 1, 'f', 'r', 'e', 'e', 0, 0, 0, 0, 0, 0, 0, uint8(free.Size + 8)}
	out = append(out, hdr...)
	out = append(out, raw[free.Offset+8:]...)
	tree2 := Scan("/mem", bytesReaderAt(out), int64(len(out)))
	got := tree2.First("free")
	if got == nil || !got.Extended || got.Size != free.Size+8 {
		t.Fatalf("extended box not detected: %+v", got)
	}
	if len(tree2.Warnings) != 0 {
		t.Fatalf("warnings after extension: %v", tree2.Warnings)
	}
}

func TestTruncatedMdatKeepsReadableSamples(t *testing.T) {
	// Truncating also shrinks the rebuilt file, so the first build reveals the
	// final length; the second build cuts 40 bytes into mdat's tail.
	spec := baseSpec(true)
	full := fixtures.Build(spec)
	spec.TruncateAt = int64(len(full)) - 40
	raw := fixtures.Build(spec)
	path := writeTemp(t, raw)
	tree, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	if len(tree.Warnings) == 0 {
		t.Fatal("truncation produced no warning")
	}
	w := tree.Warnings[0]
	if !w.Recoverable || w.Offset < 0 || w.Kind != fault.KindTruncated {
		t.Fatalf("warning not located/recoverable: %+v", w)
	}
	movie, err := ParseMovie(tree)
	if err == nil {
		t.Fatal("expected recoverable parse error for damaged file")
	}
	if ec := fault.ExitCode(err); ec != 1 {
		t.Fatalf("exit code=%d want 1", ec)
	}
	if len(movie.Tracks) != 1 {
		t.Fatalf("tracks=%d", len(movie.Tracks))
	}
	tr := movie.Tracks[0]
	if len(tr.Samples) != 11 || tr.DroppedSamples != 1 {
		t.Fatalf("verified samples=%d dropped=%d, want the 11 fully readable samples", len(tr.Samples), tr.DroppedSamples)
	}
	for _, s := range tr.Samples {
		if s.End() > int64(len(raw)) {
			t.Fatalf("sample %d ends past EOF", s.Index)
		}
	}
}

func TestBrokenStszDropsOnlyBadSample(t *testing.T) {
	spec := baseSpec(false)
	spec.BreakStszAt = 4
	raw := fixtures.Build(spec)
	path := writeTemp(t, raw)
	tree, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	movie, err := ParseMovie(tree)
	if err == nil {
		t.Fatal("expected recoverable error")
	}
	tr := movie.Tracks[0]
	// Only the oversized entry is unusable: chunk offsets are independent, so
	// the other eleven samples still resolve into mdat.
	if len(tr.Samples) != 11 || tr.DroppedSamples != 1 {
		t.Fatalf("kept=%d dropped=%d want 11/1", len(tr.Samples), tr.DroppedSamples)
	}
	for _, s := range tr.Samples {
		if s.Index == 4 {
			t.Fatalf("corrupt sample retained: %+v", s)
		}
		if !s.InMdat {
			t.Fatalf("retained sample not marked InMdat: %+v", s)
		}
	}
}

func TestBoxSizeOverrunReportsOffsetAndTrack(t *testing.T) {
	spec := baseSpec(true)
	spec.BreakSizeOf = 2
	raw := fixtures.Build(spec)
	path := writeTemp(t, raw)
	tree, _ := Open(path)
	defer tree.Close()
	movie, err := ParseMovie(tree)
	if err == nil {
		t.Fatal("expected error")
	}
	e, ok := fault.As(err)
	if !ok || !e.Recoverable {
		t.Fatalf("err=%v", err)
	}
	var located bool
	Walk(tree.Boxes, func(b *Box) bool { return true })
	for _, w := range movie.Warnings {
		if w.TrackID == 1 && w.Offset > 0 {
			located = true
		}
	}
	if !located {
		t.Fatalf("no warning carries track and offset: %+v", movie.Warnings)
	}
}

type bytesReaderAt []byte

func (b bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(b)) {
		return 0, os.ErrClosed
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, os.ErrClosed
	}
	return n, nil
}

func TestOversizedFileIsRejected(t *testing.T) {
	// Drive Open through a sparse file so the guard is exercised without
	// materialising 2 GiB of data on disk.
	if MaxFileSize != 2<<30 {
		t.Fatalf("MaxFileSize=%d", MaxFileSize)
	}
	path := filepath.Join(t.TempDir(), "big.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxFileSize + 1); err != nil {
		f.Close()
		t.Skipf("filesystem cannot create a sparse oversized file: %v", err)
	}
	f.Close()
	_, err = Open(path)
	if err == nil {
		t.Fatal("oversized file accepted")
	}
	e, ok := fault.As(err)
	if !ok || e.Kind != fault.KindParse {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(e.Error(), "above") {
		t.Fatalf("error does not name the limit: %v", e)
	}
}

func TestExtendedSizeMdatParsesAndSamplesResolve(t *testing.T) {
	for _, moovFirst := range []bool{true, false} {
		testExtendedMdat(t, moovFirst)
	}
}

func testExtendedMdat(t *testing.T, moovFirst bool) {
	t.Helper()
	spec := baseSpec(moovFirst)
	spec.ExtendedMdat = true
	raw := fixtures.Build(spec)
	path := writeTemp(t, raw)
	tree, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	if len(tree.Warnings) != 0 {
		t.Fatalf("extended mdat produced warnings: %v", tree.Warnings)
	}
	mdat := tree.First("mdat")
	if mdat == nil || !mdat.Extended || mdat.HeaderSize != 16 {
		t.Fatalf("mdat header not parsed as 64-bit: %+v", mdat)
	}
	movie, err := ParseMovie(tree)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(movie.Tracks[0].Samples); got != 12 {
		t.Fatalf("samples=%d want 12", got)
	}
	for _, s := range movie.Tracks[0].Samples {
		if !s.InMdat {
			t.Fatalf("sample %d not validated against extended mdat", s.Index)
		}
	}
}
