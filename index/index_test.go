package index

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func fp(content string) Fingerprint {
	return Fingerprint{Size: 10, Content: content, Container: "c-" + content, Metadata: "m-" + content}
}

func TestStorePutGet(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Get("/nope"); ok {
		t.Fatal("missing index should miss")
	}
	idx := &ParseIndex{ParserVersion: 1, Fingerprint: fp("a"), Tracks: []TrackIndex{
		{TrackID: 1, Handler: "vide", TimeScale: 90000, Samples: []SampleIndex{{Offset: 0, Size: 10, Sync: true}}},
	}}
	if err := st.Put("/input/a.mp4", idx); err != nil {
		t.Fatal(err)
	}
	got, ok := st.Get("/input/a.mp4")
	if !ok {
		t.Fatal("stored index should hit")
	}
	if got.ParserVersion != 1 || len(got.Tracks) != 1 || got.Tracks[0].TimeScale != 90000 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if got.CreatedUnix == 0 {
		t.Fatal("Put should stamp creation time")
	}
}

func TestStoreReuseRequiresMatchingParserVersion(t *testing.T) {
	st, _ := OpenStore(t.TempDir())
	index := &ParseIndex{ParserVersion: 1, Fingerprint: fp("a")}
	if err := st.Put("/in.mp4", index); err != nil {
		t.Fatal(err)
	}
	_, plan := st.ReuseFor("/in.mp4", fp("a"), 2)
	if plan.IndexReusable() {
		t.Fatal("a parser version bump must force re-parsing")
	}
	prev, plan := st.ReuseFor("/in.mp4", fp("a"), 1)
	if prev == nil || !plan.FullReuse() {
		t.Fatalf("same version and bytes should fully reuse: %+v", plan)
	}
}

func TestStoreAtomicOverwriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	st, _ := OpenStore(dir)
	for i := 0; i < 5; i++ {
		idx := &ParseIndex{ParserVersion: 1, Fingerprint: fp("a")}
		if err := st.Put("/in.mp4", idx); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			t.Fatalf("leftover temp file %q", e.Name())
		}
	}
}

func TestCheckpointCommitAndResume(t *testing.T) {
	dir := t.TempDir()
	st := NewCheckpointStore(filepath.Join(dir, "cp.json"))
	inputs := map[string]Fingerprint{"/a.mp4": fp("a")}
	hdr := CheckpointHeader{Inputs: inputs, ChunkSize: 64, ConfigSum: ConfigSum("cfg")}

	cp, err := st.Commit(hdr, ChunkResult{Seq: 0, Summary: "s0", Segments: 2})
	if err != nil {
		t.Fatal(err)
	}
	if cp.Header.Created == 0 {
		t.Fatal("first commit should stamp header")
	}
	if _, err := st.Commit(hdr, ChunkResult{Seq: 1, Summary: "s1", Segments: 3}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.MaxCommitted(); got != 1 {
		t.Fatalf("MaxCommitted = %d, want 1", got)
	}
	d0 := reloaded.Digest()
	// Re-committing the same chunk is idempotent.
	if _, err := st.Commit(hdr, ChunkResult{Seq: 1, Summary: "s1", Segments: 3}); err != nil {
		t.Fatalf("idempotent re-commit failed: %v", err)
	}
	again, _ := st.Load()
	if again.Digest() != d0 {
		t.Fatal("digest changed on idempotent re-commit")
	}
}

func TestCheckpointRejectsInputDrift(t *testing.T) {
	st := NewCheckpointStore(filepath.Join(t.TempDir(), "cp.json"))
	hdr := CheckpointHeader{Inputs: map[string]Fingerprint{"/a.mp4": fp("a")}, ChunkSize: 64, ConfigSum: "cfg"}
	if _, err := st.Commit(hdr, ChunkResult{Seq: 0, Summary: "s0"}); err != nil {
		t.Fatal(err)
	}
	// The same path now holds different bytes.
	drift := CheckpointHeader{Inputs: map[string]Fingerprint{"/a.mp4": fp("B")}, ChunkSize: 64, ConfigSum: "cfg"}
	_, err := st.Commit(drift, ChunkResult{Seq: 1, Summary: "s1"})
	if err == nil {
		t.Fatal("commit must refuse when an input changed since checkpoint")
	}
	// Chunk size drift is refused too.
	other := CheckpointHeader{Inputs: hdr.Inputs, ChunkSize: 32, ConfigSum: "cfg"}
	if _, err := st.Commit(other, ChunkResult{Seq: 1, Summary: "s1"}); !errors.Is(err, ErrChunkSizeChanged) {
		t.Fatalf("want ErrChunkSizeChanged, got %v", err)
	}
	// Config drift is refused.
	cfg := CheckpointHeader{Inputs: hdr.Inputs, ChunkSize: 64, ConfigSum: "different"}
	if _, err := st.Commit(cfg, ChunkResult{Seq: 1, Summary: "s1"}); !errors.Is(err, ErrConfigChanged) {
		t.Fatalf("want ErrConfigChanged, got %v", err)
	}
}

func TestCheckpointGapStopsContiguousResume(t *testing.T) {
	st := NewCheckpointStore(filepath.Join(t.TempDir(), "cp.json"))
	hdr := CheckpointHeader{Inputs: map[string]Fingerprint{}, ChunkSize: 64, ConfigSum: "cfg"}
	for _, seq := range []int{0, 1, 3} {
		if _, err := st.Commit(hdr, ChunkResult{Seq: seq, Summary: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	cp, _ := st.Load()
	if got := cp.MaxCommitted(); got != 1 {
		t.Fatalf("MaxCommitted must stop at the gap, got %d", got)
	}
}

func TestCheckpointDigestIndependentOfCommitPartitioning(t *testing.T) {
	// The digest must depend on chunk order and content only, so a run that
	// stops after 2 chunks and resumes produces the same manifest digest as a
	// continuous run.
	continuous := NewCheckpointStore(filepath.Join(t.TempDir(), "a.json"))
	hdr := CheckpointHeader{Inputs: map[string]Fingerprint{}, ChunkSize: 64, ConfigSum: "cfg"}
	results := []ChunkResult{{0, "x", 1, 0}, {1, "y", 2, 0}, {2, "z", 1, 1}, {3, "w", 0, 0}}
	for _, r := range results {
		if _, err := continuous.Commit(hdr, r); err != nil {
			t.Fatal(err)
		}
	}
	resumed := NewCheckpointStore(filepath.Join(t.TempDir(), "b.json"))
	for _, r := range results[:2] {
		if _, err := resumed.Commit(hdr, r); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate the resumed process: reload, skip committed, commit the rest.
	cp, _ := resumed.Load()
	last := cp.MaxCommitted()
	for _, r := range results {
		if r.Seq <= last {
			continue
		}
		if _, err := resumed.Commit(hdr, r); err != nil {
			t.Fatal(err)
		}
	}
	final, _ := resumed.Load()
	a, _ := continuous.Load()
	if final.Digest() != a.Digest() {
		t.Fatalf("resumed digest %s != continuous %s", final.Digest(), a.Digest())
	}
}

func TestCheckpointRejectsConflictingRewrite(t *testing.T) {
	st := NewCheckpointStore(filepath.Join(t.TempDir(), "cp.json"))
	hdr := CheckpointHeader{Inputs: map[string]Fingerprint{}, ChunkSize: 64, ConfigSum: "cfg"}
	if _, err := st.Commit(hdr, ChunkResult{Seq: 0, Summary: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Commit(hdr, ChunkResult{Seq: 0, Summary: "two"}); err == nil {
		t.Fatal("a different result for a committed chunk must be rejected")
	}
}

func TestCheckpointCorruptFileIsReported(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cp.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCheckpointStore(p).Load(); err == nil {
		t.Fatal("corrupt checkpoint must not be treated as empty progress")
	}
}

func TestCacheMetadataKeyedInvalidation(t *testing.T) {
	c, err := OpenCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &CacheEntry{Path: "/a.mp4", Metadata: "m1", Version: 1, EventsJSON: []byte(`[]`)}
	if err := c.Put(e); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("/a.mp4", "m1", 1); !ok {
		t.Fatal("matching metadata fingerprint should hit")
	}
	if _, ok := c.Get("/a.mp4", "m2", 1); ok {
		t.Fatal("a metadata change must invalidate the cache")
	}
	// A miss evicts the stale file, so an older fingerprint cannot resurrect it.
	if _, ok := c.Get("/a.mp4", "m1", 1); ok {
		t.Fatal("evicted cache entry came back")
	}
	if err := c.Put(e); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("/a.mp4", "m1", 2); ok {
		t.Fatal("parser version bump must invalidate the cache")
	}
	if err := c.Put(&CacheEntry{Path: "/b.mp4", Version: 1}); err == nil {
		t.Fatal("cache entry without metadata fingerprint must be rejected")
	}
	if err := c.Invalidate("/a.mp4"); err != nil {
		t.Fatal(err)
	}
	if err := c.Invalidate("/a.mp4"); err != nil {
		t.Fatalf("invalidate of absent entry: %v", err)
	}
}
