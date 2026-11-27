package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	"mediapipeline/internal/checkpoint"
	"mediapipeline/internal/fault"
)

func detProcess(t *testing.T, calls *int64) Process {
	t.Helper()
	return func(task Task) ([]byte, error) {
		atomic.AddInt64(calls, 1)
		return []byte(fmt.Sprintf("%s:%d:%x", task.Key, len(task.Payload), sha256.Sum256(task.Payload))), nil
	}
}

func tasksFor(n int) []Task {
	out := make([]Task, n)
	for i := range out {
		out[i] = Task{
			Key:       fmt.Sprintf("u%04d", i),
			UnitIndex: i,
			Offset:    int64(i * 10),
			Payload:   []byte{byte(i), byte(i >> 8)},
		}
	}
	return out
}

func newStore(t *testing.T) *checkpoint.Store {
	t.Helper()
	var rep fault.Report
	s, err := checkpoint.Open(filepath.Join(t.TempDir(), "state.json"), &rep)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRunProcessesAllAndIsIdempotent(t *testing.T) {
	store := newStore(t)
	var calls int64
	pool, err := New(Options{Concurrency: 4, Process: detProcess(t, &calls)}, store)
	if err != nil {
		t.Fatal(err)
	}
	tasks := tasksFor(8)
	sum, err := pool.Run(context.Background(), tasks)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Processed != 8 || sum.Replayed != 0 || len(sum.Results) != 8 {
		t.Fatalf("summary = %+v", sum)
	}
	first := OutputDigest(sum.Results)

	// Re-running the same input and parameters must not recompute anything and
	// must yield the identical summary digest.
	var calls2 int64
	pool2, _ := New(Options{Concurrency: 4, Process: detProcess(t, &calls2)}, store)
	sum2, err := pool2.Run(context.Background(), tasks)
	if err != nil {
		t.Fatal(err)
	}
	if sum2.Processed != 0 || sum2.Replayed != 8 {
		t.Fatalf("rerun recomputed: %+v", sum2)
	}
	if calls2 != 0 {
		t.Fatalf("process called %d times on a fully checkpointed run", calls2)
	}
	if got := OutputDigest(sum2.Results); got != first {
		t.Fatalf("digest changed on rerun: %s != %s", got, first)
	}
}

func TestInterruptThenResumeMatchesFullRun(t *testing.T) {
	tasks := tasksFor(10)

	fullStore := newStore(t)
	var c0 int64
	fullPool, _ := New(Options{Concurrency: 3, Process: detProcess(t, &c0)}, fullStore)
	fullSum, err := fullPool.Run(context.Background(), tasks)
	if err != nil {
		t.Fatal(err)
	}

	store := newStore(t)
	var c1 int64
	pool, _ := New(Options{InterruptAfter: 4, Process: detProcess(t, &c1)}, store)
	sum, err := pool.Run(context.Background(), tasks)
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Interrupted || sum.Processed != 4 {
		t.Fatalf("interrupt summary = %+v", sum)
	}
	if store.Len() != 4 {
		t.Fatalf("checkpoint holds %d units, want 4", store.Len())
	}

	// Resume from the persisted checkpoint.
	var rep fault.Report
	reopened, err := checkpoint.Open(store.Path(), &rep)
	if err != nil {
		t.Fatal(err)
	}
	var c2 int64
	resume, _ := New(Options{Concurrency: 3, Process: detProcess(t, &c2)}, reopened)
	sum2, err := resume.Run(context.Background(), tasks)
	if err != nil {
		t.Fatal(err)
	}
	if sum2.Processed != 6 || sum2.Replayed != 4 {
		t.Fatalf("resume summary = %+v", sum2)
	}
	if got, want := OutputDigest(sum2.Results), OutputDigest(fullSum.Results); got != want {
		t.Fatalf("resumed digest %s != full-run %s", got, want)
	}
	if reopened.Len() != 10 {
		t.Fatalf("final checkpoint holds %d", reopened.Len())
	}
}

func TestProcessErrorIsReported(t *testing.T) {
	store := newStore(t)
	process := func(task Task) ([]byte, error) {
		if task.UnitIndex == 3 {
			return nil, fmt.Errorf("boom on %s", task.Key)
		}
		return []byte("ok"), nil
	}
	pool, _ := New(Options{Concurrency: 1, Process: process}, store)
	_, err := pool.Run(context.Background(), tasksFor(5))
	if err == nil {
		t.Fatal("expected process error")
	}
	// Units completed before the failure stay committed.
	if store.Len() != 3 {
		t.Fatalf("committed %d, want 3", store.Len())
	}
}

func TestDuplicateTaskKeyRejected(t *testing.T) {
	store := newStore(t)
	var calls int64
	pool, _ := New(Options{Process: detProcess(t, &calls)}, store)
	tasks := []Task{{Key: "x"}, {Key: "x"}}
	if _, err := pool.Run(context.Background(), tasks); err == nil {
		t.Fatal("duplicate task keys must be rejected")
	}
}

func TestCancelledContextStops(t *testing.T) {
	store := newStore(t)
	var calls int64
	pool, _ := New(Options{Concurrency: 1, Process: detProcess(t, &calls)}, store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sum, err := pool.Run(ctx, tasksFor(4))
	if err == nil {
		t.Fatal("expected context error")
	}
	if sum.Processed != 0 || store.Len() != 0 {
		t.Fatalf("cancelled run did work: %+v", sum)
	}
}

func TestNilArgumentsRejected(t *testing.T) {
	store := newStore(t)
	if _, err := New(Options{}, store); err == nil {
		t.Fatal("nil process must fail")
	}
	if _, err := New(Options{Process: func(Task) ([]byte, error) { return nil, nil }}, nil); err == nil {
		t.Fatal("nil store must fail")
	}
}

func TestOutputDigestOrderIndependent(t *testing.T) {
	a := []Result{{Key: "a", Digest: "1"}, {Key: "b", Digest: "2"}}
	b := []Result{{Key: "b", Digest: "2"}, {Key: "a", Digest: "1"}}
	if OutputDigest(a) != OutputDigest(b) {
		t.Fatal("digest must not depend on result order")
	}
	if !bytes.Equal([]byte(OutputDigest(a)), []byte(OutputDigest(a))) {
		t.Fatal("digest must be stable")
	}
	// A hex digest is exactly 64 characters.
	if len(OutputDigest(a)) != 64 {
		t.Fatalf("digest length %d", len(OutputDigest(a)))
	}
	_ = hex.EncodeToString
}
