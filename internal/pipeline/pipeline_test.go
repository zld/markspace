package pipeline

import (
	"path/filepath"
	"strings"
	"testing"
)

func testKey(digest string) CacheKey {
	return CacheKey{
		InputDigest: digest,
		Params:      `{"rate":48000,"channels":2,"lufs":-16}`,
		Versions:    ToolVersions{FFmpeg: "7.1.1"},
	}
}

func TestSafeJoin(t *testing.T) {
	root := t.TempDir()
	good, err := SafeJoin(root, "a", "b", "seg.wav")
	if err != nil || !strings.HasPrefix(good, root) {
		t.Fatalf("safe join failed: %v", err)
	}
	if _, err := SafeJoin(root, "..", "escape.wav"); err == nil {
		t.Fatal("traversal outside root must be rejected")
	}
	if _, err := SafeJoin(root, filepath.Join("..", "..", "etc")); err == nil {
		t.Fatal("multi-level traversal must be rejected")
	}
}

func TestDigestStable(t *testing.T) {
	if DigestSHA256([]byte("abc")) != DigestSHA256([]byte("abc")) {
		t.Fatal("digest must be deterministic")
	}
	if DigestSHA256([]byte("abc")) == DigestSHA256([]byte("abd")) {
		t.Fatal("different inputs must differ")
	}
}

func TestCache_PutGetAndReasons(t *testing.T) {
	c, err := NewCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := testKey("src-1")
	artifact := []byte("RIFF....audio-bytes")
	entry, err := c.Put(key, "src-1", "seg0.wav", artifact)
	if err != nil {
		t.Fatal(err)
	}
	// Fresh hit: source and artifact both verify.
	got, reason, err := c.Get(key, "src-1", "seg0.wav")
	if err != nil || reason != MissNone || got.ArtifactDigest != entry.ArtifactDigest {
		t.Fatalf("cache hit failed: reason=%q err=%v", reason, err)
	}
	// Changed source digest -> source mismatch, not a key collision.
	if _, reason, err := c.Get(key, "src-2", "seg0.wav"); err != nil || reason != MissSourceMismatch {
		t.Fatalf("want source_mismatch, got %q %v", reason, err)
	}
	// Changed parameters/tool -> a different address, so nothing is cached.
	other := testKey("src-1")
	other.Params = `{"rate":44100}`
	if _, reason, err := c.Get(other, "src-1", "seg0.wav"); err != nil || reason != MissNotCached {
		t.Fatalf("want not_cached for new params, got %q %v", reason, err)
	}
	// Same address but a corrupt/stale manifest is a key mismatch.
	stale, _ := c.manifestPath(key.Hash())
	if err := writeFileAtomic(stale, []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, reason, err := c.Get(key, "src-1", "seg0.wav"); err != nil || reason != MissKeyMismatch {
		t.Fatalf("want key_mismatch on stale manifest, got %q %v", reason, err)
	}
	// A genuinely different parameter address has no manifest on disk.
	unknown := testKey("src-1")
	unknown.Params = `{"rate":8000}`
	if _, reason, err := c.Get(unknown, "src-1", "seg0.wav"); err != nil || reason != MissNotCached {
		t.Fatalf("want not_cached, got %q %v", reason, err)
	}
}

func TestCache_ArtifactTampering(t *testing.T) {
	c, _ := NewCache(t.TempDir())
	key := testKey("src-1")
	if _, err := c.Put(key, "src-1", "seg0.wav", []byte("original")); err != nil {
		t.Fatal(err)
	}
	ap, _ := c.artifactPath(key.Hash(), "seg0.wav")
	if err := writeFileAtomic(ap, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, reason, err := c.Get(key, "src-1", "seg0.wav"); err != nil || reason != MissArtifactMismatch {
		t.Fatalf("tampered artifact must mismatch, got %q %v", reason, err)
	}
}

func TestCheckpoint_IdempotentResume(t *testing.T) {
	store, err := NewCheckpointStore(filepath.Join(t.TempDir(), "cp.json"))
	if err != nil {
		t.Fatal(err)
	}
	cp, ok, err := store.Load()
	if err != nil || ok {
		t.Fatalf("fresh checkpoint must be empty: ok=%v err=%v", ok, err)
	}
	seg := SegmentRecord{Index: 0, SourceRange: [2]int64{0, 1880},
		InputDigest: "d0", OutputFile: "seg0.wav", OutputDigest: "o0", DurationMS: 1000}
	if err := store.CommitSegment(&cp, seg); err != nil {
		t.Fatal(err)
	}
	// Re-committing the same index after a retry must not duplicate it.
	if err := store.CommitSegment(&cp, seg); err != nil {
		t.Fatal(err)
	}
	if cp.SegmentCount() != 1 {
		t.Fatalf("duplicate segment committed: %d", cp.SegmentCount())
	}
	// Reopen from disk: resume sees the committed segment and skips it.
	reloaded, ok, err := store.Load()
	if err != nil || !ok || !reloaded.IsSegmentDone(0) || reloaded.SegmentCount() != 1 {
		t.Fatalf("resume state wrong: ok=%v cp=%+v err=%v", ok, reloaded, err)
	}
	if err := store.MarkComplete(&reloaded, "final-digest"); err != nil {
		t.Fatal(err)
	}
	if !reloaded.Completed || reloaded.FinalDigest != "final-digest" {
		t.Fatalf("completion not persisted: %+v", reloaded)
	}
}

func TestProvenance_Validate(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "final.wav")
	body := []byte("combined-output")
	if err := writeFileAtomic(out, body, 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewProvenance("job-1")
	p.Output = OutputProvenance{Path: out, Digest: DigestSHA256(body)}
	p.AddSegment(SegmentProvenance{Index: 0, SourceRange: [2]int64{0, 10},
		InputDigest: "in0", OutputDigest: "out0", FromCache: true})
	p.AddSegment(SegmentProvenance{Index: 1, SourceRange: [2]int64{10, 20},
		InputDigest: "in1", OutputDigest: "out1", MissReason: string(MissSourceMismatch)})
	valid, err := p.Validate(out)
	if err != nil || !valid || !p.Valid {
		t.Fatalf("valid provenance rejected: valid=%v err=%v", valid, err)
	}
	if p.Cache.Hits != 1 || p.Cache.Misses != 1 || p.Cache.Reasons[string(MissSourceMismatch)] != 1 {
		t.Fatalf("cache summary wrong: %+v", p.Cache)
	}
	// Tamper with the output: digest chain must fail validation.
	if err := writeFileAtomic(out, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if valid, _ := p.Validate(out); valid {
		t.Fatal("tampered output must fail provenance validation")
	}
}

func TestProvenance_EmptySegmentRangeRejected(t *testing.T) {
	p := NewProvenance("job-x")
	p.Output = OutputProvenance{Digest: "x"}
	p.AddSegment(SegmentProvenance{Index: 0, SourceRange: [2]int64{5, 5},
		InputDigest: "in", OutputDigest: "out"})
	if valid, _ := p.Validate(filepath.Join(t.TempDir(), "nope.wav")); valid {
		t.Fatal("empty source range must invalidate provenance")
	}
}
