package recovery

import (
	"os"
	"path/filepath"
	"testing"

	"mediabatch/internal/clock"
	"mediabatch/internal/fault"
	"mediabatch/internal/index"
)

func storeWith(t *testing.T, dir string, keys []string, chunk int, committed bool) *index.Store {
	t.Helper()
	s := &index.Store{Version: 1, Artifacts: "artifacts"}
	for _, k := range keys {
		s.Put(index.Entry{Key: k, Artifact: "artifacts/" + k + ".m4s", Chunk: chunk,
			Start: clock.Zero, End: clock.MustNew(4, 1)})
	}
	if committed {
		s.CommitChunk(chunk, "d0", keys)
	}
	if err := s.Save(dir); err != nil {
		t.Fatal(err)
	}
	return s
}

func touch(t *testing.T, dir, rel string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func inputs() []ChunkInput {
	return []ChunkInput{
		{Chunk: 0, Start: 0, End: 1, InputDigest: "d0", Artifacts: []ArtifactRef{
			{Rel: "artifacts/k1.m4s", Key: "k1"}, {Rel: "artifacts/k2.m4s", Key: "k2"}}},
		{Chunk: 1, Start: 2, End: 3, InputDigest: "d1", Artifacts: []ArtifactRef{
			{Rel: "artifacts/k3.m4s", Key: "k3"}, {Rel: "artifacts/k4.m4s", Key: "k4"}}},
	}
}

func TestAdoptUncommittedArtifacts(t *testing.T) {
	dir := t.TempDir()
	// Chunk 0 committed and present; chunk 1 artifacts on disk but no commit,
	// mirroring a crash between the artifact write and the index commit.
	s := storeWith(t, dir, []string{"k1", "k2"}, 0, true)
	for _, rel := range []string{"artifacts/k1.m4s", "artifacts/k2.m4s", "artifacts/k3.m4s", "artifacts/k4.m4s"} {
		touch(t, dir, rel)
	}
	p := Build(dir, s, inputs())
	if p.Chunks[0].Decision != Reuse {
		t.Fatalf("chunk0=%s", p.Chunks[0].Decision)
	}
	if p.Chunks[1].Decision != Adopt {
		t.Fatalf("chunk1=%s", p.Chunks[1].Decision)
	}
	if p.ResumeChunk != 1 {
		t.Fatalf("resume=%d", p.ResumeChunk)
	}
	if p.AdoptedArtifacts != 2 || p.ReusedArtifacts != 2 {
		t.Fatalf("adopted=%d reused=%d", p.AdoptedArtifacts, p.ReusedArtifacts)
	}
	if p.DuplicatePublish != 0 {
		t.Fatalf("duplicate publish=%d", p.DuplicatePublish)
	}
	if len(p.Chunks[1].Uncommitted) != 2 {
		t.Fatalf("uncommitted=%v", p.Chunks[1].Uncommitted)
	}
}

func TestRecomputeFromMissingArtifact(t *testing.T) {
	dir := t.TempDir()
	s := storeWith(t, dir, []string{"k1", "k2"}, 0, true)
	touch(t, dir, "artifacts/k1.m4s") // k2 is missing
	p := Build(dir, s, inputs())
	if p.Chunks[0].Decision != Recompute {
		t.Fatalf("chunk0=%s", p.Chunks[0].Decision)
	}
	if p.ResumeChunk != 0 || p.RecomputedSegments != 2 {
		t.Fatalf("resume=%d recomputed=%d", p.ResumeChunk, p.RecomputedSegments)
	}
	codes := map[fault.Code]int{}
	for _, i := range p.Issues {
		codes[i.Code]++
	}
	if codes[fault.CodeArtifactMissing] == 0 {
		t.Fatalf("issues=%+v", p.Issues)
	}
}

func TestRevalidateOnInputChange(t *testing.T) {
	dir := t.TempDir()
	s := storeWith(t, dir, []string{"k1", "k2"}, 0, true)
	touch(t, dir, "artifacts/k1.m4s")
	touch(t, dir, "artifacts/k2.m4s")
	in := inputs()
	in[0].InputDigest = "changed"
	p := Build(dir, s, in)
	if p.Chunks[0].Decision != Revalidate {
		t.Fatalf("decision=%s", p.Chunks[0].Decision)
	}
	if p.RevalidatedSegments != 2 {
		t.Fatalf("revalidated=%d", p.RevalidatedSegments)
	}
}

func TestFreshRunScans(t *testing.T) {
	dir := t.TempDir()
	s := storeWith(t, dir, nil, 0, false)
	p := Build(dir, s, inputs())
	if p.Chunks[0].Decision != Scan || p.ResumeChunk != 0 {
		t.Fatalf("plan=%+v", p.Chunks[0])
	}
	if p.DuplicatePublish != 0 {
		t.Fatal("fresh run must not report duplicate publishes")
	}
}
