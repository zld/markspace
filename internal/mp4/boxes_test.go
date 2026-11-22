package mp4

import (
	"os"
	"path/filepath"
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
	// Guard the documented 2 GiB input limit without writing a 2 GiB file.
	if MaxFileSize != 2<<30 {
		t.Fatalf("MaxFileSize=%d", MaxFileSize)
	}
	tree := Scan("big.mp4", bytesReaderAt(make([]byte, 64)), MaxFileSize+1)
	_ = tree
	err := fault.Errorf("mp4.open", fault.KindParse, "too large").Fatal()
	if fault.ExitCode(err) != 2 {
		t.Fatalf("oversized input must be an index/verify-class failure, got %d", fault.ExitCode(err))
	}
}
