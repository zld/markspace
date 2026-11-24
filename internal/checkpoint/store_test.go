package checkpoint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mediaaudit/internal/model"
	"mediaaudit/internal/render"
	"mediaaudit/internal/report"
	"mediaaudit/internal/timeline"
)

func chunk(idx int, n int) report.Chunk {
	items := make([]timeline.Item, n)
	for i := range items {
		items[i] = timeline.Item{
			Seq: idx*100 + i, Track: model.Track{ID: "v1", Kind: model.KindVideo},
			Kind: timeline.ItemVideo, Key: "k" + string(rune('a'+i)),
			Span:   model.Span{Start: model.RatFromInt(int64(i)), End: model.RatFromInt(int64(i + 1))},
			Source: "index", SourceIdx: i, Payload: "p",
		}
	}
	frames := []render.FrameRecord{{Point: model.RatFromInt(int64(idx)), SampleIdx: idx,
		Keyframe: true, Origin: "index", PixelHash: "px", ByteHash: "by", Width: 2, Height: 2, PayloadLen: 4}}
	c := report.Chunk{Index: idx, Items: items, Frames: frames}
	c.Finalize()
	return c
}

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestCommitWritesAtomicallyAndPersists(t *testing.T) {
	s, dir := newStore(t)
	if err := s.Begin(2, 3, "cfg1", false); err != nil {
		t.Fatal(err)
	}
	c := chunk(0, 2)
	meta, err := s.Commit(c, "in0")
	if err != nil {
		t.Fatal(err)
	}
	// Commit derives digests from the persisted payload plus the input
	// digest, so the stored digest is reproducible by Verify.
	want := report.InputDigest(meta.ItemsDigest, meta.FramesDigest, "in0")
	if meta.Digest != want {
		t.Fatalf("digest %s want %s", meta.Digest, want)
	}
	// No temporary files may survive a successful commit.
	entries, _ := os.ReadDir(filepath.Join(dir, ChunkDir))
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			t.Fatalf("leftover temp file %s", e.Name())
		}
	}
	// Reopening sees the committed chunk.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Committed() != 1 || !s2.Loaded() {
		t.Fatalf("committed=%d loaded=%v", s2.Committed(), s2.Loaded())
	}
	ok, err := s2.Verify(0, "in0")
	if err != nil || !ok {
		t.Fatalf("verify: %v %v", ok, err)
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	s, dir := newStore(t)
	if err := s.Begin(2, 2, "cfg", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(chunk(0, 2), "in0"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ChunkDir, "chunk-00000.json")
	data, _ := os.ReadFile(path)
	var p Payload
	json.Unmarshal(data, &p)
	p.Items[0].Payload = "tampered"
	out, _ := json.Marshal(p)
	os.WriteFile(path, out, 0o644)

	ok, err := s.Verify(0, "in0")
	if ok || err == nil {
		t.Fatal("tampered chunk must fail verification")
	}
	if e := err.(*model.Error); e.Code != model.CodeCheckpointCorrupt {
		t.Fatalf("code %s", e.Code)
	}
}

func TestVerifyDetectsInputChange(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Begin(2, 2, "cfg", false); err != nil {
		t.Fatal(err)
	}
	s.Commit(chunk(0, 2), "in0")
	_, err := s.Verify(0, "in-DIFFERENT")
	if err == nil {
		t.Fatal("changed inputs must refuse to resume")
	}
	if e := err.(*model.Error); e.Code != model.CodeCheckpointMismatch {
		t.Fatalf("code %s", e.Code)
	}
}

func TestBeginConfigMismatchIsFatal(t *testing.T) {
	s, _ := newStore(t)
	s.Begin(2, 2, "cfgA", false)
	s.Commit(chunk(0, 2), "in0")
	err := s.Begin(2, 2, "cfgB", true)
	if err == nil {
		t.Fatal("config change must be refused on resume")
	}
	if e := err.(*model.Error); !e.Code.Fatal() || e.Code != model.CodeCheckpointMismatch {
		t.Fatalf("code %s fatal=%v", e.Code, e.Code.Fatal())
	}
}

func TestResumeWithoutConfigChangeKeepsProgress(t *testing.T) {
	s, dir := newStore(t)
	s.Begin(2, 2, "cfg", false)
	s.Commit(chunk(0, 2), "in0")

	s2, _ := Open(dir)
	if err := s2.Begin(2, 2, "cfg", true); err != nil {
		t.Fatal(err)
	}
	if s2.Committed() != 1 {
		t.Fatalf("progress lost: %d", s2.Committed())
	}
	if _, err := s2.Commit(chunk(1, 2), "in1"); err != nil {
		t.Fatal(err)
	}
	if s2.Committed() != 2 {
		t.Fatal("second chunk not appended")
	}
}

func TestNonResumeDiscardsProgress(t *testing.T) {
	s, dir := newStore(t)
	s.Begin(2, 2, "cfg", false)
	s.Commit(chunk(0, 2), "in0")
	if err := s.Begin(2, 2, "cfg", false); err != nil {
		t.Fatal(err)
	}
	if s.Committed() != 0 {
		t.Fatal("non-resume must reset")
	}
	if _, err := os.Stat(filepath.Join(dir, ChunkDir, "chunk-00000.json")); !os.IsNotExist(err) {
		t.Fatal("stale chunk file survived reset")
	}
}

func TestCorruptManifestRejected(t *testing.T) {
	_, dir := newStore(t)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, ManifestName), []byte("{not json"), 0o644)
	if _, err := Open(dir); err == nil {
		t.Fatal("malformed manifest must be fatal")
	}
	var m Manifest
	m.Version = 999
	b, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(dir, ManifestName), b, 0o644)
	if _, err := Open(dir); err == nil {
		t.Fatal("wrong version must be fatal")
	}
}

func TestCommitOutOfOrderRejected(t *testing.T) {
	s, _ := newStore(t)
	s.Begin(2, 3, "cfg", false)
	if _, err := s.Commit(chunk(2, 2), "in2"); err == nil {
		t.Fatal("gap in chunk numbering must be rejected")
	}
}

func TestInterruptLeavesOnlyCommittedChunks(t *testing.T) {
	// Simulate an interrupted run: chunk 0 committed, chunk 1 never written.
	s, dir := newStore(t)
	s.Begin(4, 2, "cfg", false)
	s.Commit(chunk(0, 4), "in0")
	s2, _ := Open(dir)
	if s2.Committed() != 1 {
		t.Fatal("only the committed chunk must be visible")
	}
	// Resuming redoes only chunk 1.
	if err := s2.Begin(4, 2, "cfg", true); err != nil {
		t.Fatal(err)
	}
	if ok, err := s2.Verify(0, "in0"); !ok || err != nil {
		t.Fatalf("committed chunk must verify: %v", err)
	}
	if _, err := s2.Commit(chunk(1, 4), "in1"); err != nil {
		t.Fatal(err)
	}
	if s2.Committed() != 2 {
		t.Fatal("resume did not finish the batch")
	}
}
