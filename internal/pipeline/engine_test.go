package pipeline

import (
	"path/filepath"
	"testing"

	"mediavault/internal/archive"
)

func TestEngineCleanRun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "work")
	eng := NewEngine(Config{WorkDir: dir, Fault: FaultNone, ChunkSize: 4})
	m, err := eng.Run()
	if err != nil {
		t.Fatalf("clean run must succeed: %v", err)
	}
	if m.ManifestEntries == 0 || m.ChunksCommitted == 0 {
		t.Fatalf("expected rows and chunks, got %+v", m)
	}
	if m.MaxAlignmentNs > 1_000_000 {
		t.Fatalf("alignment error %d ns exceeds 1 ms", m.MaxAlignmentNs)
	}
}

func TestEngineTruncatedBoxFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "work")
	eng := NewEngine(Config{WorkDir: dir, Fault: FaultTruncatedBox, ChunkSize: 4})
	m, err := eng.Run()
	if err == nil {
		t.Fatal("truncated input must return an error")
	}
	if m == nil || len(m.Errors) == 0 {
		t.Fatal("truncation must be reported with an error view")
	}
	found := false
	for _, ev := range m.Errors {
		if ev.Kind == "truncated-box" && ev.Offset > 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a truncated-box error with a concrete offset")
	}
}

func TestEngineBoundaryJumpRepaired(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "work")
	eng := NewEngine(Config{WorkDir: dir, Fault: FaultBoundaryJump, ChunkSize: 4})
	m, err := eng.Run()
	if err != nil {
		t.Fatalf("boundary jump should be repairable: %v", err)
	}
	if len(m.Corrections) == 0 {
		t.Fatal("expected at least one boundary correction")
	}
	c := m.Corrections[0]
	if c.OffsetTicks == 0 || !c.Recovered || c.RawDTS >= c.CorrectedDTS {
		t.Fatalf("correction must be traceable and forward-shifting: %+v", c)
	}
}

func TestEngineInterruptThenResumeMatchesClean(t *testing.T) {
	base := t.TempDir()
	cleanDir := filepath.Join(base, "clean")
	intDir := filepath.Join(base, "interrupted")

	cleanEng := NewEngine(Config{WorkDir: cleanDir, Fault: FaultNone, ChunkSize: 4})
	if _, err := cleanEng.Run(); err != nil {
		t.Fatal(err)
	}

	// Interrupted boundary-jump run stops after two chunks.
	intEng := NewEngine(Config{WorkDir: intDir, Fault: FaultBoundaryJump, InterruptAfter: 2, ChunkSize: 4})
	if _, err := intEng.Run(); err != ErrInterrupted {
		t.Fatalf("want ErrInterrupted, got %v", err)
	}
	// Resume without further interruption.
	resEng := NewEngine(Config{WorkDir: intDir, Fault: FaultNone, ChunkSize: 4})
	rm, err := resEng.Run()
	if err != nil {
		t.Fatalf("resume must succeed: %v", err)
	}
	if !rm.Recovered {
		t.Fatal("resumed run must flag checkpoint recovery")
	}

	cleanIdx, err := archive.OpenIndex(cleanDir, 4)
	if err != nil {
		t.Fatal(err)
	}
	resIdx, err := archive.OpenIndex(intDir, 4)
	if err != nil {
		t.Fatal(err)
	}
	if cleanIdx.Len() != resIdx.Len() {
		t.Fatalf("manifest length clean=%d resumed=%d", cleanIdx.Len(), resIdx.Len())
	}
	cc, rc := cleanIdx.Chunks(), resIdx.Chunks()
	if len(cc) != len(rc) {
		t.Fatalf("chunk count clean=%d resumed=%d", len(cc), len(rc))
	}
	for i := range cc {
		if cc[i].SHA256 != rc[i].SHA256 {
			t.Fatalf("chunk %d hash differs after resume", i)
		}
	}

	// Re-running resume once more must not add rows or chunks.
	if _, err := resEng.Run(); err != nil {
		t.Fatal(err)
	}
	again, err := archive.OpenIndex(intDir, 4)
	if err != nil {
		t.Fatal(err)
	}
	if again.Len() != resIdx.Len() || len(again.Chunks()) != len(rc) {
		t.Fatal("re-running resume must be idempotent")
	}
}
