package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediapipeline/internal/media"
)

func testState() *State {
	return &State{
		InputSHA256:    "abc",
		ManifestSHA256: "def",
		ContentID:      "cid",
		SealedSegments: 3,
		BoundaryMS:     2000,
		Representations: []RepState{
			{Name: "400", Segments: []SegmentRef{{Index: 0, StartMS: 0, EndMS: 2000, SHA256: "s0", Bytes: 100}}},
		},
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := testState()
	if err := st.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := st.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil || got.InputSHA256 != want.InputSHA256 || got.SealedSegments != want.SealedSegments {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if got.Schema != Schema {
		t.Fatalf("schema = %d", got.Schema)
	}
}

func TestLoadMissingIsNil(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil state, got %+v", got)
	}
}

func TestSaveIsAtomicAndLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	for i := 0; i < 5; i++ {
		if err := st.Save(testState()); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("stray temp file %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Fatalf("dir has %d entries, want 1", len(entries))
	}
}

func TestCorruptCheckpointIsClassified(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(`{"schema":1,"input_sha`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ := Open(dir)
	_, err := st.Load()
	if err == nil {
		t.Fatal("expected corrupt checkpoint error")
	}
	if media.CategoryOf(err) != media.CheckpointCorrupt {
		t.Fatalf("category = %v", media.CategoryOf(err))
	}
	if media.StrategyFor(media.CategoryOf(err)) != media.StrategyResume {
		t.Fatal("corrupt checkpoints must be resumable")
	}
}

func TestUnsupportedSchemaRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"schema":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ := Open(dir)
	_, err := st.Load()
	if err == nil || media.CategoryOf(err) != media.CheckpointCorrupt {
		t.Fatalf("expected schema error, got %v", err)
	}
}

func TestIdempotencyKeyStable(t *testing.T) {
	a := IdempotencyKey("segment", "400", "0", "2000")
	b := IdempotencyKey("segment", "400", "0", "2000")
	c := IdempotencyKey("segment", "400", "1", "2000")
	if a != b {
		t.Fatal("key not stable")
	}
	if a == c {
		t.Fatal("different inputs produced the same key")
	}
	if !strings.HasPrefix(a, "segment-") {
		t.Fatalf("key = %s", a)
	}
}

func TestReuseIndexDetectsConflict(t *testing.T) {
	st := testState()
	st.Outputs = []OutputRecord{{Key: "k1", Path: "p", ContentSHA256: "h1"}}
	idx := BuildReuseIndex(st)
	prev, hit, err := idx.Lookup("k1", "h1")
	if err != nil || !hit || prev.Key != "k1" {
		t.Fatalf("lookup: %v %v %v", prev, hit, err)
	}
	_, hit, err = idx.Lookup("k1", "h2")
	if err == nil || !hit {
		t.Fatal("expected idempotency conflict")
	}
	if media.CategoryOf(err) != media.IdempotencyConflict {
		t.Fatalf("category = %v", media.CategoryOf(err))
	}
	if _, hit, err := idx.Lookup("absent", "x"); err != nil || hit {
		t.Fatalf("absent lookup: hit=%v err=%v", hit, err)
	}
}

func TestVerifyOrderingDetectsAheadCheckpoint(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	state := testState()
	state.ManifestSHA256 = "" // isolate the output check from the manifest check
	state.Outputs = []OutputRecord{{Key: "k", Path: "segments/seg0.m4s", ContentSHA256: "h", Bytes: 10}}
	// The checkpoint claims an output that was never written.
	err := st.VerifyOrdering(state)
	if err == nil || media.CategoryOf(err) != media.CheckpointAhead {
		t.Fatalf("expected checkpoint-ahead error, got %v", err)
	}
	// Write the output and it should verify.
	if err := os.MkdirAll(filepath.Join(dir, "segments"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "segments/seg0.m4s"), make([]byte, 10), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.VerifyOrdering(state); err != nil {
		t.Fatalf("VerifyOrdering after write: %v", err)
	}
}

func TestVerifyOrderingChecksManifestHash(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	manifest := []byte(`{"schema":1}`)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	state := testState()
	state.ManifestSHA256 = HashBytes(manifest)
	if err := st.VerifyOrdering(state); err != nil {
		t.Fatalf("VerifyOrdering: %v", err)
	}
	state.ManifestSHA256 = "wrong"
	if err := st.VerifyOrdering(state); err == nil {
		t.Fatal("expected manifest hash mismatch")
	}
}

func TestWriteManifestIsAtomic(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	if _, err := st.WriteManifest("manifest.json", []byte("data")); err != nil {
		t.Fatal(err)
	}
	got, err := st.ReadManifest("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "data" {
		t.Fatalf("manifest = %q", got)
	}
	if _, err := st.ReadManifest("missing.json"); media.CategoryOf(err) != media.CheckpointAhead {
		t.Fatalf("missing manifest category = %v", media.CategoryOf(err))
	}
}

func TestProvenanceDeterministicAndHashable(t *testing.T) {
	dec := []Decision{{Stage: "align", Action: "recompute", Category: "late_event", Detail: "x", Count: 2}}
	rng := TimeRange{StartNanos: 0, EndNanos: 2000, Timescale: 1000}
	a, err := NewProvenance("run1", "segment", "inhash", 100, rng, dec, []byte("out"), []string{"k1"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewProvenance("run1", "segment", "inhash", 100, rng, dec, []byte("out"), []string{"k1"})
	ha, err := a.Hash()
	if err != nil {
		t.Fatal(err)
	}
	hb, _ := b.Hash()
	if ha != hb {
		t.Fatal("provenance hash not deterministic")
	}
	c, _ := NewProvenance("run1", "segment", "inhash", 100, rng, dec, []byte("different"), []string{"k1"})
	hc, _ := c.Hash()
	if ha == hc {
		t.Fatal("provenance hash ignored output change")
	}
	if a.OutputSHA256 == c.OutputSHA256 {
		t.Fatal("output digest ignored output change")
	}
}

func TestHashFileMatchesHashBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blob")
	data := []byte("hello provenance")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h, n, err := HashFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(data)) {
		t.Fatalf("size = %d", n)
	}
	if h != HashBytes(data) {
		t.Fatal("file and byte hashes disagree")
	}
}

func TestDecisionExplain(t *testing.T) {
	d := Decision{Stage: "rtp", Action: "reorder", Category: "reordered_packet", Detail: "x", Count: 3}
	got := d.Explain()
	for _, want := range []string{"rtp", "reorder", "reordered_packet", "x3"} {
		if !strings.Contains(got, want) {
			t.Errorf("explain %q missing %q", got, want)
		}
	}
}
