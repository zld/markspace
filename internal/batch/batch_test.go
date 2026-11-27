package batch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mediabatch/internal/fixture"
	"mediabatch/internal/index"
	"mediabatch/internal/report"
)

func setup(t *testing.T, chunk int) (work string, opts Options) {
	t.Helper()
	work = t.TempDir()
	if _, err := fixture.Generate(work, fixture.Options{SegmentCount: 8, Chunk: chunk}); err != nil {
		t.Fatal(err)
	}
	return work, Options{
		WorkDir: work, InputDir: filepath.Join(work, "input"),
		ChunkSize: chunk, MaxSegmentBytes: 1 << 20,
	}
}

func countArtifacts(t *testing.T, work string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(filepath.Join(work, "artifacts"), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return n
}

func TestDemoPublishesAndReports(t *testing.T) {
	work, opts := setup(t, 3)
	run, err := Execute(opts, ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	if run.Crashed {
		t.Fatal("unexpected crash")
	}
	if run.Counts.Anomalies == 0 {
		t.Fatal("fixture anomalies should be reported")
	}
	if run.Counts.Duplicates != 1 {
		t.Fatalf("duplicates=%d", run.Counts.Duplicates)
	}
	if err := run.WriteReport(work, nil, nil); err != nil {
		t.Fatal(err)
	}
	if run.Report.MissingSource() != 0 || run.Report.SortedViolations() != 0 {
		t.Fatalf("report verify: %+v", run.Report.Summarise())
	}
	artifacts := countArtifacts(t, work)
	if artifacts == 0 {
		t.Fatal("no artifacts written")
	}
	store, err := index.Open(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Entries) != artifacts {
		t.Fatalf("index entries=%d artifacts=%d", len(store.Entries), artifacts)
	}
}

func TestCrashAfterArtifactThenResumeIsIdempotent(t *testing.T) {
	work, opts := setup(t, 3)
	opts.CrashPoint = CrashAfterArtifact
	run, err := Execute(opts, ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	if !run.Crashed {
		t.Fatal("expected injected crash")
	}
	afterCrash := countArtifacts(t, work)

	opts.CrashPoint = ""
	run2, err := Execute(opts, ModeResume)
	if err != nil {
		t.Fatal(err)
	}
	if run2.Plan.AdoptedArtifacts == 0 {
		t.Fatal("recovery should adopt the uncommitted artifacts")
	}
	if run2.Plan.DuplicatePublish != 0 {
		t.Fatalf("duplicate publish=%d", run2.Plan.DuplicatePublish)
	}
	afterResume := countArtifacts(t, work)
	if afterResume != afterCrash+run2.NewArtifacts {
		t.Fatalf("artifacts=%d crash=%d new=%d", afterResume, afterCrash, run2.NewArtifacts)
	}

	// A second resume must publish nothing.
	before := countArtifacts(t, work)
	run3, err := Execute(opts, ModeResume)
	if err != nil {
		t.Fatal(err)
	}
	if run3.NewArtifacts != 0 {
		t.Fatalf("second resume wrote %d artifacts", run3.NewArtifacts)
	}
	if after := countArtifacts(t, work); after != before {
		t.Fatalf("artifact count changed: %d -> %d", before, after)
	}
	for _, cp := range run3.Plan.Chunks {
		if cp.Decision != "reuse" {
			t.Fatalf("chunk %d decision=%s", cp.Chunk, cp.Decision)
		}
	}
}

func TestCrashAfterCommitThenResume(t *testing.T) {
	work, opts := setup(t, 2)
	opts.CrashPoint = CrashAfterCommit
	run, err := Execute(opts, ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	if !run.Crashed {
		t.Fatal("expected crash")
	}
	store, err := index.Open(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Entries) == 0 {
		t.Fatal("index commit should have happened before the crash")
	}
	opts.CrashPoint = ""
	run2, err := Execute(opts, ModeResume)
	if err != nil {
		t.Fatal(err)
	}
	if run2.Plan.AdoptedArtifacts != 0 {
		t.Fatalf("nothing should need adoption, got %d", run2.Plan.AdoptedArtifacts)
	}
	for _, cp := range run2.Plan.Chunks {
		if cp.Chunk == 0 && cp.Decision != "reuse" {
			t.Fatalf("chunk0=%s", cp.Decision)
		}
	}
}

func TestChangedInputRevalidatesChunk(t *testing.T) {
	work, opts := setup(t, 3)
	if _, err := Execute(opts, ModeDemo); err != nil {
		t.Fatal(err)
	}
	// Replace one segment's bytes in place. Its path and timing are unchanged,
	// so only the content digest can detect the change.
	target := filepath.Join(work, "input", "hls", "seg-2.m4s")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0xFF
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatal(err)
	}
	run, err := Execute(opts, ModeResume)
	if err != nil {
		t.Fatal(err)
	}
	if run.Plan.RevalidatedSegments == 0 {
		t.Fatal("changed input must revalidate its block")
	}
	revalidated := 0
	for _, cp := range run.Plan.Chunks {
		if cp.Decision == "revalidate" {
			revalidated++
		}
	}
	if revalidated != 1 {
		t.Fatalf("revalidated chunks=%d", revalidated)
	}
}

func TestMissingArtifactRecomputes(t *testing.T) {
	work, opts := setup(t, 3)
	run, err := Execute(opts, ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	// Remove an artifact from a middle chunk.
	var victim string
	for _, o := range run.Outcomes {
		if o.Artifact != "" && o.Chunk == 1 {
			victim = filepath.Join(work, filepath.FromSlash(o.Artifact))
			break
		}
	}
	if victim == "" {
		t.Fatal("no artifact in chunk 1")
	}
	if err := os.Remove(victim); err != nil {
		t.Fatal(err)
	}
	run2, err := Execute(opts, ModeResume)
	if err != nil {
		t.Fatal(err)
	}
	if run2.Plan.ResumeChunk != 1 {
		t.Fatalf("resume chunk=%d", run2.Plan.ResumeChunk)
	}
	if run2.Plan.RecomputedSegments == 0 {
		t.Fatal("expected recomputation")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("artifact should be restored: %v", err)
	}
}

func TestChunkSizeValidation(t *testing.T) {
	_, opts := setup(t, 3)
	opts.ChunkSize = 0
	if _, err := Execute(opts, ModeDemo); err == nil {
		t.Fatal("expected chunk size error")
	}
}

// Verify must report a persisted report whose entries are out of start-time
// order; sorting before the check would hide the defect.
func TestVerifyDetectsUnsortedPersistedReport(t *testing.T) {
	work, opts := setup(t, 3)
	run, err := Execute(opts, ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.WriteReport(work, nil, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(work, "report.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rep report.Report
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(rep.Entries)-1; i < j; i, j = i+1, j-1 {
		rep.Entries[i], rep.Entries[j] = rep.Entries[j], rep.Entries[i]
	}
	data, err = json.Marshal(&rep)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	vr, err := Verify(work)
	if err != nil {
		t.Fatal(err)
	}
	if vr.SortedViolations == 0 {
		t.Fatal("reversed entries must be reported as order violations")
	}
}
