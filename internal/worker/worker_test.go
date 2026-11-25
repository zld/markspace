package worker

import (
	"os"
	"path/filepath"
	"testing"

	"rtpregress/internal/media"
)

// fakeStages returns stages that write deterministic bytes derived from their
// name plus the shared input digest, and count how often they ran.
func fakeStages(calls map[string]int) map[string]StageFunc {
	mk := func(name string) StageFunc {
		return func(in StageInput) (StageOutput, error) {
			calls[name]++
			prior := []byte("begin")
			if in.Stage != StageParse {
				p, err := in.PriorOutput(Stages[StageIndex(in.Stage)-1])
				if err != nil {
					return StageOutput{}, err
				}
				prior = p
			}
			out := append(append([]byte(name+"|"), prior...), []byte("|"+in.InputDigest)...)
			return StageOutput{Bytes: out}, nil
		}
	}
	return map[string]StageFunc{
		StageParse: mk(StageParse), StageAlign: mk(StageAlign),
		StageGenerate: mk(StageGenerate), StageVerify: mk(StageVerify),
	}
}

func newRunner(t *testing.T, dir, key, input, params, interrupt string, calls map[string]int) *Runner {
	t.Helper()
	return NewRunner(Options{
		Dir: dir, IdemKey: key, InputDigest: input, ParamsDigest: params,
		InterruptAfter: interrupt,
	}, fakeStages(calls))
}

func TestRunAllStagesAndCommitManifest(t *testing.T) {
	dir := t.TempDir()
	calls := map[string]int{}
	r := newRunner(t, dir, "run-1", "sha256:aa", "sha256:pp", "", calls)
	m, err := r.Run(RunSummary{})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Completed {
		t.Fatal("manifest not completed")
	}
	if len(m.Stages) != 4 {
		t.Fatalf("stages = %d", len(m.Stages))
	}
	for i, rec := range m.Stages {
		if rec.Name != Stages[i] {
			t.Fatalf("stage %d = %s", i, rec.Name)
		}
		if rec.Attempt != 1 {
			t.Fatalf("stage %s attempts = %d", rec.Name, rec.Attempt)
		}
	}
	for _, s := range Stages {
		if calls[s] != 1 {
			t.Fatalf("stage %s ran %d times", s, calls[s])
		}
	}
}

func TestResumeRerunsOnlyRemainingStages(t *testing.T) {
	dir := t.TempDir()
	calls := map[string]int{}
	// Interrupt after align: parse and align commit, then the run stops.
	r := newRunner(t, dir, "run-1", "sha256:aa", "sha256:pp", StageAlign, calls)
	_, err := r.Run(RunSummary{})
	if err == nil {
		t.Fatal("expected interrupt")
	}
	if media.KindOf(err) != media.KindInterrupt {
		t.Fatalf("kind = %s", media.KindOf(err))
	}
	if calls[StageParse] != 1 || calls[StageAlign] != 1 {
		t.Fatalf("committed stages ran: %v", calls)
	}
	if calls[StageGenerate] != 0 || calls[StageVerify] != 0 {
		t.Fatalf("later stages should not run: %v", calls)
	}

	// Resume without the interrupt: only generate and verify re-run.
	r2 := newRunner(t, dir, "run-1", "sha256:aa", "sha256:pp", "", calls)
	m, err := r2.Run(RunSummary{})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Completed {
		t.Fatal("resume did not complete")
	}
	if calls[StageParse] != 1 || calls[StageAlign] != 1 {
		t.Fatalf("committed stages re-ran: %v", calls)
	}
	if calls[StageGenerate] != 1 || calls[StageVerify] != 1 {
		t.Fatalf("remaining stages did not run: %v", calls)
	}
}

func TestResumeManifestMatchesContinuous(t *testing.T) {
	continuous := t.TempDir()
	calls := map[string]int{}
	mc, err := newRunner(t, continuous, "run-1", "sha256:aa", "sha256:pp", "", calls).Run(RunSummary{})
	if err != nil {
		t.Fatal(err)
	}

	resumed := t.TempDir()
	calls2 := map[string]int{}
	_, err = newRunner(t, resumed, "run-1", "sha256:aa", "sha256:pp", StageAlign, calls2).Run(RunSummary{})
	if media.KindOf(err) != media.KindInterrupt {
		t.Fatalf("expected interrupt, got %v", err)
	}
	mr, err := newRunner(t, resumed, "run-1", "sha256:aa", "sha256:pp", "", calls2).Run(RunSummary{})
	if err != nil {
		t.Fatal(err)
	}
	// The stage sequence and digests must match exactly; the only difference
	// is the summary's Resumed flag, which the caller owns.
	if len(mc.Stages) != len(mr.Stages) {
		t.Fatalf("stage counts differ: %d vs %d", len(mc.Stages), len(mr.Stages))
	}
	for i := range mc.Stages {
		a, b := mc.Stages[i], mr.Stages[i]
		if a.Name != b.Name || a.InputDigest != b.InputDigest ||
			a.OutputDigest != b.OutputDigest || a.Attempt != b.Attempt {
			t.Fatalf("stage %d differs:\n continuous %+v\n resumed    %+v", i, a, b)
		}
	}
}

func TestResumeRecomputesTamperedStageOutput(t *testing.T) {
	dir := t.TempDir()
	calls := map[string]int{}
	if _, err := newRunner(t, dir, "run-1", "sha256:aa", "sha256:pp", StageAlign, calls).Run(RunSummary{}); err != nil &&
		media.KindOf(err) != media.KindInterrupt {
		t.Fatalf("interrupt run: %v", err)
	}
	// Corrupt align's committed output behind the manifest's back. Resume
	// must notice the digest mismatch and recompute rather than trust the
	// manifest index.
	alignPath := filepath.Join(dir, "stage-align.out")
	if err := os.WriteFile(alignPath, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := newRunner(t, dir, "run-1", "sha256:aa", "sha256:pp", "", calls).Run(RunSummary{})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Completed {
		t.Fatal("run did not complete")
	}
	if calls[StageAlign] != 2 {
		t.Fatalf("align ran %d times, want 2 (recomputed)", calls[StageAlign])
	}
	if calls[StageParse] != 1 {
		t.Fatalf("parse re-ran despite intact output: %d", calls[StageParse])
	}
	// The published digest must describe the recomputed bytes.
	data, err := os.ReadFile(alignPath)
	if err != nil {
		t.Fatal(err)
	}
	if Digest(data) != m.Stages[StageIndex(StageAlign)].OutputDigest {
		t.Fatal("manifest digest does not match the recomputed stage output")
	}
}

func TestResumeWithoutCommittedManifestStartsFresh(t *testing.T) {
	dir := t.TempDir()
	calls := map[string]int{}
	// An empty directory has no manifest; the first run is not a resume.
	m, err := newRunner(t, dir, "run-1", "sha256:aa", "sha256:pp", "", calls).Run(RunSummary{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Summary.Resumed {
		t.Fatal("fresh run marked as resumed")
	}
}

func TestDuplicateDeliveryIsNoOp(t *testing.T) {
	dir := t.TempDir()
	calls := map[string]int{}
	r := newRunner(t, dir, "run-1", "sha256:aa", "sha256:pp", "", calls)
	m1, err := r.Run(RunSummary{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(r.ManifestPath())
	if err != nil {
		t.Fatal(err)
	}
	// Second delivery of the same input and params must not run stages again
	// and must leave the manifest bytes identical.
	m2, err := r.Run(RunSummary{})
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(r.ManifestPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("manifest changed on re-delivery:\nbefore %s\nafter %s", before, after)
	}
	if len(m2.Stages) != len(m1.Stages) {
		t.Fatal("stage count changed")
	}
	for s, n := range calls {
		if n != 1 {
			t.Fatalf("stage %s ran %d times", s, n)
		}
	}
}

func TestConflictOnDifferentInput(t *testing.T) {
	dir := t.TempDir()
	calls := map[string]int{}
	if _, err := newRunner(t, dir, "run-1", "sha256:aa", "sha256:pp", "", calls).Run(RunSummary{}); err != nil {
		t.Fatal(err)
	}
	// Same key, different input digest: refuse and do not overwrite.
	_, err := newRunner(t, dir, "run-1", "sha256:bb", "sha256:pp", "", calls).Run(RunSummary{})
	if err == nil {
		t.Fatal("expected conflict")
	}
	if media.KindOf(err) != media.KindConflict {
		t.Fatalf("kind = %s", media.KindOf(err))
	}
	m, err := LoadManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m.InputDigest != "sha256:aa" {
		t.Fatalf("manifest was overwritten: input = %s", m.InputDigest)
	}
}

func TestConflictOnDifferentParams(t *testing.T) {
	dir := t.TempDir()
	calls := map[string]int{}
	if _, err := newRunner(t, dir, "run-1", "sha256:aa", "sha256:pp", "", calls).Run(RunSummary{}); err != nil {
		t.Fatal(err)
	}
	_, err := newRunner(t, dir, "run-1", "sha256:aa", "sha256:qq", "", calls).Run(RunSummary{})
	if err == nil {
		t.Fatal("expected conflict")
	}
	if media.KindOf(err) != media.KindConflict {
		t.Fatalf("kind = %s", media.KindOf(err))
	}
}

func TestConcurrentDeliverySingleWriter(t *testing.T) {
	dir := t.TempDir()
	calls := map[string]int{}
	// Two runners race on the same key. The lock admits one; the other must
	// fail cleanly rather than write a partial manifest.
	r1 := newRunner(t, dir, "run-1", "sha256:aa", "sha256:pp", "", calls)
	lock := filepath.Join(dir, ".lock")
	// Hold the lock as an external process would, then attempt a run.
	if _, err := acquireLock(lock); err != nil {
		// Recreate the directory state by hand.
	}
	// The lock created above is released immediately; simulate contention by
	// creating the file directly.
	os.Remove(lock)
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r1.Run(RunSummary{}); err == nil {
		t.Fatal("expected lock contention error")
	} else if media.KindOf(err) != media.KindResume {
		t.Fatalf("kind = %s", media.KindOf(err))
	}
	os.Remove(lock)
	if _, err := r1.Run(RunSummary{}); err != nil {
		t.Fatal(err)
	}
}

func TestManifestIsReplacedAtomically(t *testing.T) {
	dir := t.TempDir()
	calls := map[string]int{}
	r := newRunner(t, dir, "run-1", "sha256:aa", "sha256:pp", StageAlign, calls)
	_, err := r.Run(RunSummary{})
	if media.KindOf(err) != media.KindInterrupt {
		t.Fatalf("expected interrupt, got %v", err)
	}
	// After the interrupt the manifest must be valid JSON describing exactly
	// the committed prefix; no temporary files may remain.
	m, err := LoadManifest(r.ManifestPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Stages) != 2 || m.Completed {
		t.Fatalf("partial manifest = %+v", m)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "manifest.json" && e.Name() != "summary.json" &&
			e.Name() != "stage-parse.out" && e.Name() != "stage-align.out" {
			t.Fatalf("unexpected file %q left behind", e.Name())
		}
	}
}

func TestParamsDigestIsOrderIndependent(t *testing.T) {
	a := ParamsDigest(map[string]string{"budget": "500", "bytes": "4096", "quality": "0.95"})
	b := ParamsDigest(map[string]string{"quality": "0.95", "bytes": "4096", "budget": "500"})
	if a != b {
		t.Fatalf("digest depends on map order: %s vs %s", a, b)
	}
	c := ParamsDigest(map[string]string{"budget": "500", "bytes": "4096", "quality": "0.96"})
	if a == c {
		t.Fatal("different params produced the same digest")
	}
}

func TestLoadManifestRejectsCorruptAndWrongVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(path); err == nil || media.KindOf(err) != media.KindResume {
		t.Fatalf("corrupt manifest not refused: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(path); err == nil || media.KindOf(err) != media.KindResume {
		t.Fatalf("wrong version not refused: %v", err)
	}
}
