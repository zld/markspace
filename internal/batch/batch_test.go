package batch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/clock"
	"mediad/internal/mederr"
)

func testPlan(t *testing.T) []PlanChunk {
	t.Helper()
	plan, err := PlanChunks(clock.Span{Start: 0, End: 3_000_000_000}, timeSecond)
	if err != nil {
		t.Fatal(err)
	}
	for i := range plan {
		plan[i].RelPath = fmt.Sprintf("chunks/chunk-%03d.bin", i)
	}
	return plan
}

const timeSecond = int64(1_000_000_000)

func testRunner(dir string) *Runner {
	return &Runner{
		Dir:         dir,
		Fingerprint: "fp-test",
		Render: func(p PlanChunk, tmp string) error {
			return os.WriteFile(tmp, []byte(fmt.Sprintf("MAGIC chunk=%d fp-test", p.Index)), 0o644)
		},
		Verify: func(path string, p PlanChunk) error {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if len(b) < 12 || string(b[:5]) != "MAGIC" {
				return errors.New("missing MAGIC header")
			}
			return nil
		},
	}
}

func TestChunkCommitReuseAndTruncationRedo(t *testing.T) {
	dir := t.TempDir()
	r := testRunner(dir)
	plan := testPlan(t)

	idx, rep, err := r.Run(plan)
	if err != nil {
		t.Fatal(err)
	}
	if rep.NewlyCommitted != 3 || rep.Reused != 0 || len(idx.Chunks) != 3 {
		t.Fatalf("bad first run: rep=%+v chunks=%d", rep, len(idx.Chunks))
	}

	_, rep2, err := r.Run(plan)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Reused != 3 || rep2.NewlyCommitted != 0 {
		t.Fatalf("idempotent rerun not all reused: %+v", rep2)
	}

	target := filepath.Join(dir, plan[1].RelPath)
	if err := os.Truncate(target, 5); err != nil {
		t.Fatal(err)
	}
	_, rep3, err := r.Run(plan)
	if err != nil {
		t.Fatal(err)
	}
	if rep3.Reused != 2 || len(rep3.Truncated) != 1 || rep3.Truncated[0] != 1 {
		t.Fatalf("truncated chunk not isolated/redone: %+v", rep3)
	}
}

func TestCorruptIndexRebuildsAndOverlapPreservesCheckpoint(t *testing.T) {
	dir := t.TempDir()
	r := testRunner(dir)
	plan := testPlan(t)
	idx, _, err := r.Run(plan)
	if err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(dir, IndexName)
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}

	late := clock.Span{Start: 1_500_000_000, End: 2_500_000_000}
	err = CheckLateOverlap(idx, "late:1", late)
	var me *mederr.Error
	if !errors.As(err, &me) || me.Category != mederr.CatOverlap {
		t.Fatalf("expected overlap error, got %v", err)
	}
	afterOverlap, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(afterOverlap) {
		t.Fatal("overlap rejection modified checkpoint")
	}
	if err := CheckLateOverlap(idx, "late:ok", clock.Span{Start: 3_000_000_000, End: 4_000_000_000}); err != nil {
		t.Fatalf("adjacent interval should not overlap: %v", err)
	}

	if err := os.WriteFile(indexPath, []byte("{ truncated"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, rep, err := r.Run(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.IndexRebuilt || rep.NewlyCommitted != 3 {
		t.Fatalf("corrupt index did not rebuild chunks: %+v", rep)
	}
	if _, err := os.Stat(indexPath + ".corrupt"); err != nil {
		t.Fatalf("corrupt index not preserved: %v", err)
	}
}

func TestFingerprintMismatchRefuses(t *testing.T) {
	dir := t.TempDir()
	r := testRunner(dir)
	plan := testPlan(t)
	if _, _, err := r.Run(plan); err != nil {
		t.Fatal(err)
	}
	r.Fingerprint = "other"
	_, _, err := r.Run(plan)
	var me *mederr.Error
	if !errors.As(err, &me) || me.Category != mederr.CatIndex {
		t.Fatalf("expected index fingerprint error, got %v", err)
	}
}

func TestNormalizePlanDuplicateRules(t *testing.T) {
	plan := testPlan(t)
	withDup := append([]PlanChunk{}, plan...)
	withDup = append(withDup, plan[1])
	got, dupes, err := NormalizePlan(withDup)
	if err != nil || dupes != 1 || len(got) != len(plan) {
		t.Fatalf("exact duplicate not deduplicated: got=%d dupes=%d err=%v", len(got), dupes, err)
	}

	conflict := append([]PlanChunk{}, plan...)
	bad := plan[1]
	bad.Span.End++
	conflict = append(conflict, bad)
	if _, _, err := NormalizePlan(conflict); err == nil {
		t.Fatal("expected conflicting duplicate chunk error")
	}
}
