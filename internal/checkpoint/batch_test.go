package checkpoint

import (
	"os"
	"path/filepath"
	"testing"

	"mediabatch/internal/clock"
)

func TestPlan(t *testing.T) {
	chunks, err := Plan(7, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 {
		t.Fatalf("chunks=%d", len(chunks))
	}
	if chunks[2].First != 6 || chunks[2].Last != 6 || chunks[2].Count() != 1 {
		t.Fatalf("last=%+v", chunks[2])
	}
	if _, err := Plan(7, 0); err == nil {
		t.Fatal("expected chunk size error")
	}
	if _, err := Plan(-1, 3); err == nil {
		t.Fatal("expected negative count error")
	}
	if chunks, err := Plan(0, 3); err != nil || len(chunks) != 0 {
		t.Fatalf("empty plan: %v %v", chunks, err)
	}
}

func TestDigestSensitivity(t *testing.T) {
	mk := func(hash string) []Identity {
		return []Identity{
			{Manifest: "m", Representation: "a", Segment: "s1", Start: clock.Zero, End: clock.MustNew(4, 1), Path: "p1", ContentHash: hash},
			{Manifest: "m", Representation: "a", Segment: "s2", Start: clock.MustNew(4, 1), End: clock.MustNew(8, 1), Path: "p2", ContentHash: "h2"},
		}
	}
	a := Digest(mk("h1"))
	if a != Digest(mk("h1")) {
		t.Fatal("digest must be deterministic")
	}
	if a == Digest(mk("hX")) {
		t.Fatal("content change must change the digest")
	}
	chunks, _ := Plan(2, 1)
	AssignDigests(chunks, mk("h1"))
	chunks2, _ := Plan(2, 1)
	AssignDigests(chunks2, mk("hX"))
	if chunks[0].InputDigest == chunks2[0].InputDigest {
		t.Fatal("changed input must invalidate the chunk")
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := &State{Version: version, ChunkSize: 3, CrashedChunk: -1}
	st.Begin(0)
	if err := st.Save(dir); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Running || got.CrashedChunk != 0 {
		t.Fatalf("state=%+v", got)
	}
	got.Finish(0)
	if err := got.Save(dir); err != nil {
		t.Fatal(err)
	}
	again, _ := Load(dir)
	if again.Running || again.Completed != 1 || again.CrashedChunk != -1 {
		t.Fatalf("state=%+v", again)
	}
	if again.Summarize() == "" {
		t.Fatal("empty summary")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

func TestLoadCorrupt(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(Path(dir), []byte("nope"), 0o644)
	if _, err := Load(dir); err == nil {
		t.Fatal("expected decode error")
	}
	os.WriteFile(Path(dir), []byte(`{"version":42}`), 0o644)
	if _, err := Load(dir); err == nil {
		t.Fatal("expected version error")
	}
}
