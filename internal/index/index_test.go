package index_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"medtrace/internal/core"
	"medtrace/internal/fixture"
	"medtrace/internal/index"
	"medtrace/internal/manifest"
)

// buildScenario generates the seeded media tree and returns the scenario and
// its root. Every test gets an isolated tree under t.TempDir.
func buildScenario(t *testing.T) (*fixture.Scenario, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "media")
	sc, err := fixture.Build(root, 23)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return sc, root
}

func loadPlaylist(t *testing.T, root, rel string) *manifest.Playlist {
	t.Helper()
	pl, err := manifest.Load(root, rel)
	if err != nil {
		t.Fatalf("load %s: %v", rel, err)
	}
	return pl
}

func TestBuildDashAssignsEpochsAndKeyframes(t *testing.T) {
	sc, root := buildScenario(t)
	pl := loadPlaylist(t, root, sc.DashMPD)
	idx, anomalies, err := index.Build(root, pl)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(idx.Segments) != 7 {
		t.Fatalf("segments = %d, want 7", len(idx.Segments))
	}
	if err := idx.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	for _, seg := range idx.Segments {
		if len(seg.Samples) != sc.SamplesPerSeg {
			t.Errorf("%s: samples = %d", seg.URI, len(seg.Samples))
		}
		if len(seg.Keyframes) != 1 || seg.Keyframes[0] != 0 {
			t.Errorf("%s: keyframes = %v, want [0]", seg.URI, seg.Keyframes)
		}
		if len(seg.SHA256) != 64 {
			t.Errorf("%s: sha256 = %q", seg.URI, seg.SHA256)
		}
		if seg.End <= seg.Start {
			t.Errorf("%s: empty canonical range [%s,%s)", seg.URI, seg.Start, seg.End)
		}
	}
	// The representation switch opens the second epoch and is reported as a
	// time jump at the first high-representation fragment.
	if len(idx.Epochs) != 2 {
		t.Fatalf("epochs = %d, want 2", len(idx.Epochs))
	}
	if idx.Epochs[1].Reason != core.EpochRepresentation {
		t.Errorf("epoch 1 reason = %q", idx.Epochs[1].Reason)
	}
	var switchAnomaly *core.Anomaly
	for i := range anomalies {
		if anomalies[i].Reason == string(core.EpochRepresentation) {
			switchAnomaly = &anomalies[i]
		}
	}
	if switchAnomaly == nil {
		t.Fatal("no representation-switch anomaly")
	}
	if switchAnomaly.Segment != "dash/video/v-high/seg-001.m4s" {
		t.Errorf("switch segment = %q", switchAnomaly.Segment)
	}
	// Canonical time is continuous across the switch: the high
	// representation starts exactly where the low one ended.
	if idx.Segments[4].Start != idx.Segments[3].End {
		t.Errorf("switch gap: %s -> %s", idx.Segments[3].End, idx.Segments[4].Start)
	}
	// Sample canonical positions never regress.
	last := core.Nanos(-1)
	for _, seg := range idx.Segments {
		for _, s := range seg.Samples {
			if s.Canonical < last {
				t.Fatalf("canonical regressed at %s@%d", seg.URI, s.ByteOffset)
			}
			last = s.Canonical
		}
	}
}

func TestBuildHLSDiscontinuityAndWrap(t *testing.T) {
	sc, root := buildScenario(t)
	pl := loadPlaylist(t, root, sc.HLSPlaylist)
	idx, anomalies, err := index.Build(root, pl)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(idx.Segments) != 4 {
		t.Fatalf("segments = %d, want 4", len(idx.Segments))
	}
	if err := idx.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	wantReasons := []core.EpochReason{core.EpochStart, core.EpochDiscontinuity, core.EpochWrap}
	if len(idx.Epochs) != len(wantReasons) {
		t.Fatalf("epochs = %d, want %d", len(idx.Epochs), len(wantReasons))
	}
	for i, want := range wantReasons {
		if idx.Epochs[i].Reason != want {
			t.Errorf("epoch %d reason = %q, want %q", i, idx.Epochs[i].Reason, want)
		}
	}
	// Both jumps are anomalies carrying the raw tick that triggered them.
	raws := map[int64]bool{}
	for _, a := range anomalies {
		if a.Category == core.CatTimeJump {
			raws[a.RawTimestamp] = true
		}
	}
	if !raws[4_294_000_000] || !raws[200] {
		t.Errorf("time-jump raw timestamps = %v", raws)
	}
	// Canonical time stays monotonic across the discontinuity and the wrap.
	for i := 1; i < len(idx.Segments); i++ {
		if idx.Segments[i].Start < idx.Segments[i-1].End {
			t.Errorf("segment %d starts before the previous ends", i)
		}
	}
}

func TestCacheReuseAndInvalidation(t *testing.T) {
	sc, root := buildScenario(t)
	pl := loadPlaylist(t, root, sc.DashMPD)
	idx, _, err := index.Build(root, pl)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	digest, err := index.InputDigest(root, pl)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	cache := index.NewCache(filepath.Join(t.TempDir(), "index"))
	entry := index.BuildEntry(pl, digest, idx, idx.Epochs)
	if err := cache.Save(entry); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := cache.Load(pl.File)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded == nil {
		t.Fatal("cache entry missing after save")
	}
	if !loaded.Valid(digest) {
		t.Error("unchanged input did not reuse the cache")
	}
	if len(loaded.Segments) != len(idx.Segments) {
		t.Errorf("cached segments = %d, want %d", len(loaded.Segments), len(idx.Segments))
	}
	// An unknown manifest has no entry, and that is not an error.
	missing, err := cache.Load("nope.mpd")
	if err != nil || missing != nil {
		t.Errorf("missing entry = %v, %v", missing, err)
	}
	// Touching one referenced segment must change the digest and invalidate
	// the cached entry.
	abs, err := core.SafeJoin(root, pl.Segments[0].URI)
	if err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(abs, later, later); err != nil {
		t.Fatal(err)
	}
	changed, err := index.InputDigest(root, pl)
	if err != nil {
		t.Fatalf("digest after touch: %v", err)
	}
	if changed == digest {
		t.Error("changed input produced an identical digest")
	}
	if loaded.Valid(changed) {
		t.Error("stale cache accepted a changed input")
	}
}

func TestRescanRoundTripsLayout(t *testing.T) {
	sc, root := buildScenario(t)
	for _, lay := range sc.Dash[:2] {
		got, err := index.Rescan(root, lay.URI)
		if err != nil {
			t.Fatalf("rescan %s: %v", lay.URI, err)
		}
		if len(got) != len(lay.Samples) {
			t.Fatalf("%s: samples = %d, want %d", lay.URI, len(got), len(lay.Samples))
		}
		for i, s := range lay.Samples {
			g := got[i]
			if g.Offset != s.Offset || int(g.Size) != s.Size ||
				g.DTSTicks != s.DTSTicks || g.DurTicks != s.DurTicks || g.Key != s.Key {
				t.Errorf("%s sample %d: got %+v, want %+v", lay.URI, i, g, s)
			}
		}
	}
}
