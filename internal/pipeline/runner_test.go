package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func paths(t *testing.T) (cpPath, cacheDir string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "checkpoint.json"), filepath.Join(dir, "cache")
}

func workFunc(calls map[int]int) SegmentWork {
	return func(index int) ([]byte, string, error) {
		calls[index]++
		content := []byte{byte(index), 0x10, 0x20}
		return content, DigestBytes(content), nil
	}
}

func TestRunnerResumeRecomputesOnlyPending(t *testing.T) {
	cpPath, _ := paths(t)
	cfg := RunConfig{TotalSegments: 10}
	calls := map[int]int{}
	r, err := NewRunner(cpPath, "input", "config", cfg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(cfg, workFunc(calls))
	if err != nil {
		t.Fatal(err)
	}
	if res.Completed != 10 || res.Recomputed != 10 || res.Reused != 0 {
		t.Fatalf("first run = %+v", res)
	}
	// Simulate interruption after 2 segments by constructing a checkpoint.
	cp := NewCheckpoint("input", "config")
	for i := 0; i < 2; i++ {
		_ = cp.MarkCompleted(i, DigestBytes([]byte{byte(i), 0x10, 0x20}))
	}
	if err := cp.Save(cpPath); err != nil {
		t.Fatal(err)
	}
	calls2 := map[int]int{}
	recomputeCalls := map[int]int{}
	cfg2 := RunConfig{
		TotalSegments: 10,
		Recompute: func(index int) (string, error) {
			recomputeCalls[index]++
			return DigestBytes([]byte{byte(index), 0x10, 0x20}), nil
		},
	}
	r2, err := NewRunner(cpPath, "input", "config", cfg2)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := r2.Run(cfg2, workFunc(calls2))
	if err != nil {
		t.Fatal(err)
	}
	if res2.Reused != 2 || res2.Recomputed != 8 {
		t.Fatalf("resume = %+v", res2)
	}
	if len(calls2) != 8 {
		t.Fatalf("work called for %d segments, want 8", len(calls2))
	}
	for i := 0; i < 2; i++ {
		if calls2[i] != 0 {
			t.Fatalf("completed segment %d was recomputed", i)
		}
		if recomputeCalls[i] != 1 {
			t.Fatalf("completed segment %d was not digest-verified", i)
		}
	}
}

func TestRunnerRejectsStaleCheckpoint(t *testing.T) {
	cpPath, _ := paths(t)
	cp := NewCheckpoint("different-input", "config")
	if err := cp.Save(cpPath); err != nil {
		t.Fatal(err)
	}
	_, err := NewRunner(cpPath, "input", "config", RunConfig{})
	if !errors.Is(err, ErrStaleCheckpoint) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunnerRejectsCorruptCompletedDigest(t *testing.T) {
	cpPath, _ := paths(t)
	cp := NewCheckpoint("input", "config")
	_ = cp.MarkCompleted(0, "deadbeef")
	if err := cp.Save(cpPath); err != nil {
		t.Fatal(err)
	}
	cfg := RunConfig{TotalSegments: 1, Recompute: func(int) (string, error) {
		return DigestBytes([]byte("real")), nil
	}}
	_, err := NewRunner(cpPath, "input", "config", cfg)
	if !errors.Is(err, ErrCorruptCheckpoint) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunnerRejectionLeavesSegmentPending(t *testing.T) {
	cpPath, _ := paths(t)
	cfg := RunConfig{TotalSegments: 4}
	r, err := NewRunner(cpPath, "input", "config", cfg)
	if err != nil {
		t.Fatal(err)
	}
	work := func(index int) ([]byte, string, error) {
		if index == 2 {
			return nil, "", &RejectError{Reason: "representations not alignable"}
		}
		return []byte{byte(index)}, DigestBytes([]byte{byte(index)}), nil
	}
	res, err := r.Run(cfg, work)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rejected != 1 || res.Completed != 3 {
		t.Fatalf("result = %+v", res)
	}
	cp, err := LoadCheckpoint(cpPath, "input", "config")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cp.CompletedSet()[2]; ok {
		t.Fatal("rejected segment marked completed")
	}
	if len(cp.Rejections) != 1 || cp.Rejections[0].Index != 2 {
		t.Fatalf("rejections = %+v", cp.Rejections)
	}
	// A rerun retries only the rejected segment.
	calls := map[int]int{}
	r2, err := NewRunner(cpPath, "input", "config", RunConfig{TotalSegments: 4})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r2.Run(RunConfig{TotalSegments: 4}, workFunc(calls)); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[2] != 1 {
		t.Fatalf("retry calls = %+v", calls)
	}
}

func TestRunnerAbortDoesNotCommitFailingSegment(t *testing.T) {
	cpPath, _ := paths(t)
	cfg := RunConfig{TotalSegments: 5}
	r, err := NewRunner(cpPath, "input", "config", cfg)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("disk full")
	work := func(index int) ([]byte, string, error) {
		if index == 3 {
			return nil, "", boom
		}
		return []byte{byte(index)}, DigestBytes([]byte{byte(index)}), nil
	}
	_, err = r.Run(cfg, work)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	cp, err := LoadCheckpoint(cpPath, "input", "config")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cp.CompletedSet()[3]; ok {
		t.Fatal("failing segment was committed")
	}
	if len(cp.Completed) != 3 {
		t.Fatalf("completed = %d, want 3", len(cp.Completed))
	}
}

func TestCheckpointIdempotentMark(t *testing.T) {
	cp := NewCheckpoint("i", "c")
	if err := cp.MarkCompleted(1, "aaa"); err != nil {
		t.Fatal(err)
	}
	if err := cp.MarkCompleted(1, "aaa"); err != nil {
		t.Fatalf("idempotent mark failed: %v", err)
	}
	if err := cp.MarkCompleted(1, "bbb"); err == nil {
		t.Fatal("conflicting digest accepted")
	}
	if len(cp.Completed) != 1 {
		t.Fatalf("completed = %d", len(cp.Completed))
	}
}

func TestCheckpointDeterministicSave(t *testing.T) {
	cpPath, _ := paths(t)
	cp := NewCheckpoint("i", "c")
	_ = cp.MarkCompleted(3, DigestBytes([]byte("three")))
	_ = cp.MarkCompleted(1, DigestBytes([]byte("one")))
	if err := cp.Save(cpPath); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(cpPath)
	for i := 0; i < 5; i++ {
		if err := cp.Save(cpPath); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(cpPath)
		if string(got) != string(first) {
			t.Fatalf("checkpoint bytes drift on save")
		}
	}
}
