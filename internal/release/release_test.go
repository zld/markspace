package release

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"releasepipe/internal/errs"
	"releasepipe/internal/index"
	"releasepipe/internal/ingest"
)

func fakeInputs() (*ingest.Timeline, []string, map[string]string, map[string]map[string]string) {
	mk := func(kind, src string) *ingest.Track {
		return &ingest.Track{
			ID: kind, Kind: kind, SourcePath: src,
			Samples: []ingest.Sample{{DTS: 0, PTS: 0, Duration: 100}},
			Params:  map[string]string{"handler": kind, "timescale": "1000"},
		}
	}
	tl := &ingest.Timeline{Tracks: []*ingest.Track{
		mk("audio", "main.mp4"), mk("transition", "transition.mp4"), mk("video", "main.mp4"),
	}}
	platforms := []string{"ios", "android"}
	srcDigests := map[string]string{"main.mp4": "sd-main", "transition.mp4": "sd-trans"}
	meta := map[string]map[string]string{
		"main.mp4":       {"major_brand": "isom"},
		"transition.mp4": {"major_brand": "isom"},
	}
	return tl, platforms, srcDigests, meta
}

func TestBuildAssetsDeterministic(t *testing.T) {
	tl, platforms, srcDigests, meta := fakeInputs()
	a1 := BuildAssets(tl, platforms, srcDigests, meta)
	a2 := BuildAssets(tl, platforms, srcDigests, meta)
	if len(a1) != 6 {
		t.Fatalf("assets = %d, want 6", len(a1))
	}
	for i := range a1 {
		if a1[i] != a2[i] {
			t.Fatal("asset list not deterministic")
		}
		if i > 0 && a1[i-1].ID >= a1[i].ID {
			t.Fatal("assets not sorted by ID")
		}
	}
	// A source-byte change invalidates exactly that source's assets.
	srcDigests["main.mp4"] = "sd-changed"
	a3 := BuildAssets(tl, platforms, srcDigests, meta)
	changed := 0
	for i := range a1 {
		if a1[i].Digest != a3[i].Digest {
			changed++
			if a1[i].Kind == "transition" {
				t.Fatal("unrelated asset invalidated")
			}
		}
	}
	if changed != 4 { // audio+video x 2 platforms
		t.Fatalf("changed = %d, want 4", changed)
	}
	// A pure muxing change (same bytes, new container meta) invalidates.
	srcDigests["main.mp4"] = "sd-main"
	meta["main.mp4"]["major_brand"] = "mp42"
	a4 := BuildAssets(tl, platforms, srcDigests, meta)
	if a4[0].Digest == a1[0].Digest {
		t.Fatal("muxing change did not invalidate")
	}
}

func runToCompletion(t *testing.T, dir string, stopAfter int, resume bool) *Result {
	t.Helper()
	tl, platforms, srcDigests, meta := fakeInputs()
	assets := BuildAssets(tl, platforms, srcDigests, meta)
	store, err := index.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Process(dir, assets, platforms, srcDigests, store, stopAfter, resume)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestProcessInterruptAndResume(t *testing.T) {
	dir := t.TempDir()
	// Interrupted run: stops after block 2, nothing committed.
	res := runToCompletion(t, dir, 2, false)
	if !res.Interrupted || res.NextBlock != 2 {
		t.Fatalf("interrupted=%v next=%d", res.Interrupted, res.NextBlock)
	}
	store, _ := index.OpenStore(dir)
	if store.CommitCount() != 0 {
		t.Fatal("interrupted run must not commit the index")
	}
	// Resume: continues from block 3 (index 2) and completes.
	res = runToCompletion(t, dir, 0, true)
	if res.Interrupted || res.ResumedFrom != 2 {
		t.Fatalf("resumed from %d", res.ResumedFrom)
	}
	if len(res.Records) != 6 {
		t.Fatalf("records = %d", len(res.Records))
	}
	// Reference run without interruption: identical manifest.
	ref := runToCompletion(t, t.TempDir(), 0, false)
	if res.ManifestSHA != ref.ManifestSHA {
		t.Fatal("resumed manifest differs from uninterrupted run")
	}
	// Repeat run: everything reused, identical manifest, no duplicates.
	again := runToCompletion(t, dir, 0, false)
	if again.Rebuilt != 0 || again.Reused != 6 {
		t.Fatalf("rerun reused=%d rebuilt=%d", again.Reused, again.Rebuilt)
	}
	if again.ManifestSHA != ref.ManifestSHA {
		t.Fatal("rerun produced a different manifest")
	}
	seen := map[string]bool{}
	for _, r := range again.Records {
		if seen[r.ID] {
			t.Fatalf("duplicate asset %s", r.ID)
		}
		seen[r.ID] = true
	}
	store, _ = index.OpenStore(dir)
	if store.CommitCount() != 2 {
		t.Fatalf("commits = %d, want 2 (one per completed run)", store.CommitCount())
	}
}

func TestRollback(t *testing.T) {
	dir := t.TempDir()
	m1 := []byte(`{"v":1}`)
	m2 := []byte(`{"v":2}`)
	if _, err := CommitVersion(dir, m1, index.HashBytes(m1)); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitVersion(dir, m2, index.HashBytes(m2)); err != nil {
		t.Fatal(err)
	}
	cur, err := CurrentVersion(dir)
	if err != nil || cur != 2 {
		t.Fatalf("current = %d", cur)
	}
	v, err := Rollback(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v.Number != 1 || v.ManifestSHA != index.HashBytes(m1) {
		t.Fatalf("rolled back to %+v", v)
	}
	// No further complete version: stable error code.
	if _, err := Rollback(dir); err == nil {
		t.Fatal("expected NO_ROLLBACK_TARGET")
	} else {
		var pe *errs.Error
		if !errors.As(err, &pe) || pe.Code != errs.CodeNoRollback {
			t.Fatalf("want NO_ROLLBACK_TARGET, got %v", err)
		}
	}
}

func TestRollbackSkipsCorruptAndIncomplete(t *testing.T) {
	dir := t.TempDir()
	m1 := []byte(`{"v":1}`)
	if _, err := CommitVersion(dir, m1, index.HashBytes(m1)); err != nil {
		t.Fatal(err)
	}
	// v2: incomplete (crashed mid-publish); v3: corrupt manifest.
	incomplete := Version{Number: 2, ManifestSHA: "x", Complete: false,
		Manifest: `{"v":2}`}
	raw, _ := json.Marshal(incomplete)
	if err := os.MkdirAll(filepath.Join(dir, "releases"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(versionPath(dir, 2), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	corrupt := Version{Number: 3, ManifestSHA: "deadbeef", Complete: true,
		Manifest: `{"v":3}`}
	raw, _ = json.Marshal(corrupt)
	if err := os.WriteFile(versionPath(dir, 3), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "releases", "current"), []byte("3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := Rollback(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v.Number != 1 {
		t.Fatalf("rollback landed on v%d, want v1 (only complete+valid)", v.Number)
	}
}
