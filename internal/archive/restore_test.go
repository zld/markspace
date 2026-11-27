package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/ingest"
	"mediad/internal/manifest"
	"mediad/internal/model"
	"mediad/internal/pipeline"
)

// stageFuncs returns one function per ordered stage, each writing a fixed
// artifact, so committed digests are deterministic.
func stageFuncs(t *testing.T) map[string]pipeline.StageFunc {
	t.Helper()
	funcs := map[string]pipeline.StageFunc{}
	for _, name := range pipeline.OrderedStages {
		name := name
		funcs[name] = func(_ context.Context, _ *pipeline.State, _ string, workDir string) error {
			return os.WriteFile(filepath.Join(workDir, name+".out"), []byte("art-"+name), 0o644)
		}
	}
	return funcs
}

// setupArchive builds a work root, immutable source and a single-entry
// manifest, returning the layout and the real source digest.
func setupArchive(t *testing.T, codec string) (Layout, string) {
	t.Helper()
	root := t.TempDir()
	layout := Layout{
		WorkRoot:     filepath.Join(root, "work"),
		StreamPath:   filepath.Join(root, "stream.bin"),
		ManifestPath: filepath.Join(root, "manifest.json"),
	}
	if err := os.WriteFile(layout.StreamPath, []byte("source-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _, _, err := ingest.HashFile(context.Background(), layout.StreamPath, ingest.ReaderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	realSHA := ingest.HexDigest(sum)
	mf := manifest.New()
	if err := mf.Add(&manifest.Entry{
		SourceSHA: realSHA, Codec: codec, StreamSHA: realSHA, TrackDigest: "t",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.WriteAtomically(filepath.Dir(layout.ManifestPath),
		filepath.Base(layout.ManifestPath), mf); err != nil {
		t.Fatal(err)
	}
	return layout, realSHA
}

func TestVerifySuccess(t *testing.T) {
	layout, realSHA := setupArchive(t, "h264")
	eng, _ := pipeline.NewEngine(layout.WorkRoot)
	runner := pipeline.NewRunner(eng, stageFuncs(t))
	if _, err := runner.Run(context.Background(), realSHA, "h264"); err != nil {
		t.Fatal(err)
	}
	v, err := NewVerifier(layout, ingest.ReaderConfig{ChunkSize: 1024, QueueDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	res, err := v.Verify(context.Background(), realSHA, "h264")
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if res.StagesVerified != len(pipeline.OrderedStages) || !res.ManifestPresent {
		t.Fatalf("verify result wrong: %+v", res)
	}
	if res.ArtifactsOK == 0 {
		t.Fatal("no artifacts reported as verified")
	}
}

func TestVerifyDetectsSourceTampering(t *testing.T) {
	layout, realSHA := setupArchive(t, "av1")
	if err := os.WriteFile(layout.StreamPath, []byte("tampered-source"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, _ := NewVerifier(layout, ingest.ReaderConfig{})
	_, err := v.Verify(context.Background(), realSHA, "av1")
	se, ok := model.AsStructured(err)
	if !ok || se.Code != model.ErrDigestMismatch || se.ByteOffset != 0 {
		t.Fatalf("want DIGEST_MISMATCH at offset 0, got %v", err)
	}
}

func TestVerifyMissingManifestEntry(t *testing.T) {
	layout, realSHA := setupArchive(t, "h265")
	// Remove the manifest entry by writing a manifest keyed to another source.
	mf := manifest.New()
	_ = mf.Add(&manifest.Entry{SourceSHA: "different-source", Codec: "h265", StreamSHA: "x", TrackDigest: "t"})
	if _, err := manifest.WriteAtomically(filepath.Dir(layout.ManifestPath),
		filepath.Base(layout.ManifestPath), mf); err != nil {
		t.Fatal(err)
	}
	v, _ := NewVerifier(layout, ingest.ReaderConfig{})
	_, err := v.Verify(context.Background(), realSHA, "h265")
	se, ok := model.AsStructured(err)
	if !ok || se.Code != model.ErrStageState {
		t.Fatalf("want STAGE_STATE_INVALID for missing entry, got %v", err)
	}
}

func TestVerifyIncompleteStage(t *testing.T) {
	layout, realSHA := setupArchive(t, "h264")
	// Run only the first stage by registering just ingest.
	eng, _ := pipeline.NewEngine(layout.WorkRoot)
	onlyIngest := map[string]pipeline.StageFunc{
		pipeline.StageIngest: stageFuncs(t)[pipeline.StageIngest],
	}
	runner := pipeline.NewRunner(eng, onlyIngest)
	if _, err := runner.Run(context.Background(), realSHA, "h264"); err == nil {
		t.Fatal("run with missing stage functions should fail")
	}
	v, _ := NewVerifier(layout, ingest.ReaderConfig{})
	_, err := v.Verify(context.Background(), realSHA, "h264")
	if err == nil {
		t.Fatal("incomplete archive must not verify")
	}
}

func TestReconcileResumesAndVerifies(t *testing.T) {
	layout, realSHA := setupArchive(t, "av1")
	eng, _ := pipeline.NewEngine(layout.WorkRoot)
	runner := pipeline.NewRunner(eng, stageFuncs(t))
	v, _ := NewVerifier(layout, ingest.ReaderConfig{})
	st, res, err := v.Reconcile(context.Background(), realSHA, "av1", runner)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if st.NextPending() != "" || res.StagesVerified != len(pipeline.OrderedStages) {
		t.Fatal("reconcile did not finish and verify all stages")
	}
}

func TestEnsureNoDuplicateEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dup.json")
	raw := `{"version":1,"entries":[{"source_sha256":"dup","codec":"h264"},{"source_sha256":"dup","codec":"h264"}]}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	err := EnsureNoDuplicateEntries(path)
	se, ok := model.AsStructured(err)
	if !ok || se.Code != model.ErrDuplicateManifest {
		t.Fatalf("want duplicate error, got %v", err)
	}
	clean := manifest.New()
	_ = clean.Add(&manifest.Entry{SourceSHA: "a", Codec: "h264"})
	_ = clean.Add(&manifest.Entry{SourceSHA: "b", Codec: "av1"})
	p2, _ := manifest.WriteAtomically(dir, "clean.json", clean)
	if err := EnsureNoDuplicateEntries(p2); err != nil {
		t.Fatalf("clean manifest must pass: %v", err)
	}
}

func TestCleanUncommitted(t *testing.T) {
	root := t.TempDir()
	workRoot := filepath.Join(root, "work")
	eng, _ := pipeline.NewEngine(workRoot)
	// Create both an uncommitted running dir and a committed dir.
	if err := os.MkdirAll(eng.StageDir("s1", pipeline.StageIngest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(eng.CommittedDir("s1", pipeline.StageNormalize), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CleanUncommitted(workRoot, "s1"); err != nil {
		t.Fatal(err)
	}
	if Exists(eng.StageDir("s1", pipeline.StageIngest)) {
		t.Fatal("uncommitted dir should be removed")
	}
	if !Exists(eng.CommittedDir("s1", pipeline.StageNormalize)) {
		t.Fatal("committed dir must survive cleanup")
	}
}
