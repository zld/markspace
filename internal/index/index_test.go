package index_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mp4seek/internal/box"
	"mp4seek/internal/export"
	"mp4seek/internal/fixture"
	"mp4seek/internal/index"
	"mp4seek/internal/movie"
	"mp4seek/internal/seek"
)

func loadFixture(t *testing.T, path string) (*box.Reader, *movie.Movie, []*index.TrackIndex) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	rd := box.NewReader(f, st.Size())
	m, err := movie.Parse(rd)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := index.Build(m, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	return rd, m, idx
}

func generate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := fixture.Generate(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestBuildIndexEditWrap(t *testing.T) {
	dir := generate(t)
	_, m, idx := loadFixture(t, filepath.Join(dir, "edit-wrap.mp4"))

	if m.Timescale != 1000 || len(m.Tracks) != 2 {
		t.Fatalf("movie: timescale=%d tracks=%d", m.Timescale, len(m.Tracks))
	}
	t1 := idx[0]
	if len(t1.Samples) != 8 || t1.SyncCount != 2 || t1.Wraps != 1 {
		t.Fatalf("track1: samples=%d sync=%d wraps=%d", len(t1.Samples), t1.SyncCount, t1.Wraps)
	}
	// Unwrapped DTS must survive the 32-bit wraparound monotonically.
	for i := 1; i < len(t1.Samples); i++ {
		if t1.Samples[i].DTS < t1.Samples[i-1].DTS {
			t.Fatalf("DTS not monotonic at %d: %v", i, t1.Samples)
		}
	}
	if t1.Samples[6].DTS != 4295012296 {
		t.Fatalf("sample 6 DTS=%d, want 4295012296 (post-wrap)", t1.Samples[6].DTS)
	}
	// PTS = DTS + CTS, not a B-frame display time mistaken for DTS.
	if t1.Samples[0].PTS != 18000 || t1.Samples[2].PTS != 4294661296 {
		t.Fatalf("PTS[0]=%d PTS[2]=%d", t1.Samples[0].PTS, t1.Samples[2].PTS)
	}
	// Track 2: stss present but empty -> no keyframes.
	if idx[1].SyncCount != 0 {
		t.Fatalf("track2 sync=%d, want 0", idx[1].SyncCount)
	}
}

func TestLocateByTime(t *testing.T) {
	dir := generate(t)
	_, m, idx := loadFixture(t, filepath.Join(dir, "edit-wrap.mp4"))

	// 12.5 s movie time -> 7.5 s media time -> sync sample 0.
	res, err := seek.LocateTime(idx[0], 12500, m.Timescale)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sample != 0 || !res.Sync || res.PTS != 18000 || res.MediaTime != 675000 {
		t.Fatalf("got %+v", res)
	}
}

func TestLocateHoleAndBeyondEnd(t *testing.T) {
	dir := generate(t)
	_, m, idx := loadFixture(t, filepath.Join(dir, "edit-wrap.mp4"))

	_, err := seek.LocateTime(idx[0], 2500, m.Timescale)
	if se, ok := err.(*seek.Error); !ok || se.Status != seek.StatusHole {
		t.Fatalf("want hole, got %v", err)
	}
	if !seek.IsNotLocatable(err) {
		t.Fatal("hole must be a not-locatable error")
	}
	_, err = seek.LocateTime(idx[0], 25000, m.Timescale)
	if se, ok := err.(*seek.Error); !ok || se.Status != seek.StatusBeyondEnd {
		t.Fatalf("want beyond-end, got %v", err)
	}
}

func TestLocateNoSyncTrack(t *testing.T) {
	dir := generate(t)
	_, m, idx := loadFixture(t, filepath.Join(dir, "edit-wrap.mp4"))

	_, err := seek.LocateTime(idx[1], 1000, m.Timescale)
	if se, ok := err.(*seek.Error); !ok || se.Status != seek.StatusNoSync {
		t.Fatalf("want no-sync, got %v", err)
	}
}

func TestLocateSampleFallback(t *testing.T) {
	dir := generate(t)
	_, _, idx := loadFixture(t, filepath.Join(dir, "edit-wrap.mp4"))

	res, err := seek.LocateSample(idx[0], 3)
	if err != nil || res.Sample != 0 {
		t.Fatalf("sample 3 -> %+v, err=%v", res, err)
	}
	res, err = seek.LocateSample(idx[0], 6)
	if err != nil || res.Sample != 4 {
		t.Fatalf("sample 6 -> %+v, err=%v", res, err)
	}
	if _, err := seek.LocateSample(idx[0], 99); err == nil {
		t.Fatal("want out-of-range error")
	}
}

func TestExportHashMatchesPayload(t *testing.T) {
	dir := generate(t)
	rd, m, idx := loadFixture(t, filepath.Join(dir, "edit-wrap.mp4"))

	res, err := seek.LocateTime(idx[0], 12500, m.Timescale)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := export.NewRecord(rd, idx[0], res)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.SHA256) != 64 {
		t.Fatalf("hash %q is not 64 hex chars", rec.SHA256)
	}
	buf := make([]byte, res.Size)
	if _, err := rd.ReadAt(buf, res.Offset); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf)
	if rec.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash mismatch: %s vs %x", rec.SHA256, sum)
	}
	if rec.FrameType != "sync" || rec.Track != 1 {
		t.Fatalf("got %+v", rec)
	}
}

func TestDeterministicFixtures(t *testing.T) {
	d1, d2 := generate(t), generate(t)
	b1, err := os.ReadFile(filepath.Join(d1, "edit-wrap.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(filepath.Join(d2, "edit-wrap.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(b1) != sha256.Sum256(b2) {
		t.Fatal("fixture generation is not deterministic")
	}
}

func TestTruncatedBoxError(t *testing.T) {
	dir := generate(t)
	f, err := os.Open(filepath.Join(dir, "truncated.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	rd := box.NewReader(f, st.Size())
	_, err = movie.Parse(rd)
	if err == nil || !strings.Contains(err.Error(), `"moov"`) || !strings.Contains(err.Error(), "offset 32") {
		t.Fatalf("error must name box type and offset, got %v", err)
	}
}

func TestFragmentedRejected(t *testing.T) {
	dir := generate(t)
	f, err := os.Open(filepath.Join(dir, "frag.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	rd := box.NewReader(f, st.Size())
	m, err := movie.Parse(rd)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Fragmented {
		t.Fatal("frag.mp4 must be flagged fragmented")
	}
}

func TestCrossCheckAgrees(t *testing.T) {
	dir := generate(t)
	p := filepath.Join(dir, "edit-wrap.mp4")
	_, m, idx := loadFixture(t, p)
	diags, err := export.CrossCheck(p, m, idx)
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
}
