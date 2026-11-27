package index_test

import (
	"os"
	"path/filepath"
	"testing"

	"mediasync/internal/fixture"
	"mediasync/internal/index"
	"mediasync/internal/model"
)

func testTracks() []fixture.TrackSpec {
	return []fixture.TrackSpec{
		{
			Handler:   "soun",
			TrackID:   1,
			Timescale: 48000,
			Deltas:    []uint32{4800, 4800, 4800, 4800, 4800}, // 5 samples, 100 ms each
		},
		{
			Handler:   "subt",
			TrackID:   2,
			Timescale: 1000,
			Deltas:    []uint32{100, 100, 100, 100, 100}, // 5 samples, 100 ms each
		},
	}
}

func TestScanHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.mp4")
	if _, err := fixture.WriteMP4(path, 1000, testTracks()); err != nil {
		t.Fatal(err)
	}
	res, err := index.Scan(path, model.DefaultLimits())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Tracks) != 2 {
		t.Fatalf("want 2 tracks, got %d", len(res.Tracks))
	}
	for _, tr := range res.Tracks {
		if tr.SampleCount != 5 {
			t.Fatalf("track %d: want 5 samples, got %d", tr.TrackID, tr.SampleCount)
		}
		first := tr.Samples[0]
		if first.StartNS != 0 {
			t.Fatalf("first sample starts at %d, want 0", first.StartNS)
		}
		// Each sample is 100 ms; timescales differ but ticks must agree.
		if got := tr.Samples[1].StartNS; got != 100*model.Milli {
			t.Fatalf("track %d second sample at %d, want %d", tr.TrackID, got, 100*model.Milli)
		}
		if tr.EndNS != 500*model.Milli {
			t.Fatalf("track %d end %d, want %d", tr.TrackID, tr.EndNS, 500*model.Milli)
		}
		if first.Offset < 0 || first.Size <= 0 {
			t.Fatalf("bad sample boundary: %+v", first)
		}
	}
	if res.InputSHA256 == "" || len(res.InputSHA256) != 64 {
		t.Fatalf("input digest missing: %q", res.InputSHA256)
	}
}

func TestScanDeterminism(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "a.mp4")
	p2 := filepath.Join(dir, "b.mp4")
	data, err := fixture.WriteMP4(p1, 1000, testTracks())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p2, data, 0o644); err != nil {
		t.Fatal(err)
	}
	r1, err := index.Scan(p1, model.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	r2, err := index.Scan(p2, model.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if r1.InputSHA256 != r2.InputSHA256 {
		t.Fatal("identical bytes must produce identical input digests")
	}
}

func TestScanTruncatedContainer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cut.mp4")
	data, err := fixture.WriteMP4(path, 1000, testTracks())
	if err != nil {
		t.Fatal(err)
	}
	// Cut inside the moov/mdat region but keep ftyp intact.
	if err := fixture.Truncate(path, int64(len(data))-40); err != nil {
		t.Fatal(err)
	}
	_, err = index.Scan(path, model.DefaultLimits())
	me := model.AsMediaError(err)
	if me == nil || (me.Class != model.ClassTruncated && me.Class != model.ClassCorruptBox) {
		t.Fatalf("want truncated/corrupt structured error, got %v", err)
	}
	if me.Offset < 0 {
		t.Fatalf("truncation error must carry a byte offset: %+v", me)
	}
}

func TestInvalidTimebaseIsolated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "zero-ts.mp4")
	specs := []fixture.TrackSpec{
		{Handler: "soun", TrackID: 1, Timescale: 0, Deltas: []uint32{100, 100}},
		{Handler: "subt", TrackID: 2, Timescale: 1000, Deltas: []uint32{100, 100}},
	}
	if _, err := fixture.WriteMP4(path, 1000, specs); err != nil {
		t.Fatal(err)
	}
	res, err := index.Scan(path, model.DefaultLimits())
	if err != nil {
		t.Fatalf("a bad track must not abort the scan: %v", err)
	}
	if len(res.Tracks) != 1 || res.Tracks[0].TrackID != 2 {
		t.Fatalf("only the valid track should survive: %+v", res.Tracks)
	}
	if len(res.Anomalies) != 1 || res.Anomalies[0].Class != model.ClassInvalidTimebase {
		t.Fatalf("want one invalid_timebase anomaly, got %+v", res.Anomalies)
	}
}

func TestFileSizeLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.mp4")
	if _, err := fixture.WriteMP4(path, 1000, testTracks()); err != nil {
		t.Fatal(err)
	}
	limits := model.DefaultLimits()
	if _, err := index.Scan(path, limits); err != nil {
		t.Fatalf("default limits must accept fixture: %v", err)
	}
	limits.MaxFileBytes = 128
	if _, err := index.Scan(path, limits); model.AsMediaError(err) == nil ||
		model.AsMediaError(err).Class != model.ClassLimitExceeded {
		t.Fatalf("want limit_exceeded, got %v", err)
	}
	limits.MaxFileBytes = model.MaxFileBytes + 1
	if err := limits.Validate(); model.AsMediaError(err) == nil ||
		model.AsMediaError(err).Class != model.ClassLimitExceeded {
		t.Fatalf("over-hard-ceiling limit must be rejected, got %v", err)
	}
}

func TestStoreRoundTripAndReuse(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "ok.mp4")
	idxPath := filepath.Join(dir, "idx.json")
	if _, err := fixture.WriteMP4(media, 1000, testTracks()); err != nil {
		t.Fatal(err)
	}
	res, err := index.Scan(media, model.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	digest, err := index.SaveAtomic(idxPath, res)
	if err != nil {
		t.Fatal(err)
	}
	loaded, loadedDigest, err := index.LoadIfFresh(idxPath, media)
	if err != nil {
		t.Fatalf("fresh load: %v", err)
	}
	if loaded == nil || loadedDigest != digest {
		t.Fatal("fresh index must be reused with its digest")
	}

	// Mutate the container: same path, different bytes -> rebuild required.
	if err := os.WriteFile(media, []byte("totally different bytes now"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale, _, err := index.LoadIfFresh(idxPath, media)
	if err != nil {
		t.Fatalf("stale input must be reported as not-fresh, not an error: %v", err)
	}
	if stale != nil {
		t.Fatal("changed input must invalidate the cached index")
	}
}

func TestStoreDetectsCorruptIndex(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "ok.mp4")
	idxPath := filepath.Join(dir, "idx.json")
	if _, err := fixture.WriteMP4(media, 1000, testTracks()); err != nil {
		t.Fatal(err)
	}
	res, err := index.Scan(media, model.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.SaveAtomic(idxPath, res); err != nil {
		t.Fatal(err)
	}
	// Truncated JSON simulates a crash mid-write (atomic rename should
	// normally prevent this, but defense in depth is the contract).
	raw, err := os.ReadFile(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(idxPath, raw[:len(raw)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := index.Load(idxPath); model.AsMediaError(err) == nil ||
		model.AsMediaError(err).Class != model.ClassIndexCorrupt {
		t.Fatalf("want index_corrupt, got %v", err)
	}
}
