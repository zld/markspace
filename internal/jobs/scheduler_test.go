package jobs

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"rateqa/internal/media"
	"rateqa/internal/store"
)

func testParams(n int) store.Parameters {
	return store.Parameters{SourceFingerprint: "fp-test", Width: 64, Height: 48,
		FPSNum: 25, FPSDen: 1, Segments: n, TargetKbps: 600,
		FramesPerSegment: 25, TotalFrames: n * 25}
}

func testSpecs(n int) []SegmentSpec {
	out := make([]SegmentSpec, n)
	for i := 0; i < n; i++ {
		ref := media.SegmentRef{Index: i, Frames: 25, Width: 64, Height: 48,
			FPSNum: 25, FPSDen: 1, Path: "virtual.y4m"}
		out[i] = SegmentSpec{Ref: ref, TargetKbps: 600}
	}
	return out
}

// fakeExecutor models b = b0*exp(-k*qp), 25 frames at 25fps => 1s duration.
// A small delay leaves candidates genuinely in flight when fail-after fires,
// mirroring real ffmpeg subprocess latency.
func fakeExecutor(mu *sync.Mutex, calls map[int]int, failOnce map[int]bool) Executor {
	return func(_ context.Context, spec SegmentSpec, qp int) (ExecResult, error) {
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		calls[qp]++
		if failOnce[qp] {
			delete(failOnce, qp)
			mu.Unlock()
			return ExecResult{}, errors.New("simulated ffmpeg failure")
		}
		mu.Unlock()
		dur := spec.Ref.Header().DurationSeconds(spec.Ref.Frames) // 1.0s
		kbps := 2000 * math.Exp(-0.06*float64(qp))
		bytes := int64(kbps * 1000 * dur / 8)
		return ExecResult{Bytes: bytes, Stats: media.FrameStats{
			Frames: 25, PSNRMean: 48 - float64(qp), PSNRMin: 40 - float64(qp),
			PSNRMax: 50 - float64(qp), PSNRStd: 2.5,
		}}, nil
	}
}

func TestSchedulerRunAndWorkerCap(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir, testParams(3))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mu := &sync.Mutex{}
	calls := map[int]int{}
	sched := New(st, dir, Config{Workers: 9}, fakeExecutor(mu, calls, nil))
	if sched.Workers() != MaxWorkers {
		t.Fatalf("worker cap: got %d", sched.Workers())
	}
	results, err := sched.Run(context.Background(), testSpecs(3))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("results: %d", len(results))
	}
	for _, r := range results {
		if len(r.Candidates) < 3 {
			t.Errorf("segment %d has %d candidates", r.Index, len(r.Candidates))
		}
		errPct := 100 * (r.Selected.BitrateKbps - 600) / 600
		if math.Abs(errPct) > 15 {
			t.Errorf("segment %d bitrate error %.1f%%", r.Index, errPct)
		}
	}
	// No QP executed twice within one segment.
	mu.Lock()
	for qp, n := range calls {
		if n > 3 {
			t.Errorf("qp %d executed %d times across 3 segments", qp, n)
		}
	}
	mu.Unlock()
}

func TestSchedulerRetryThenSuccess(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir, testParams(1))
	defer st.Close()
	mu := &sync.Mutex{}
	failOnce := map[int]bool{16: true}
	sched := New(st, dir, Config{Workers: 1},
		fakeExecutor(mu, map[int]int{}, failOnce))
	if _, err := sched.Run(context.Background(), testSpecs(1)); err != nil {
		t.Fatalf("retry should recover: %v", err)
	}
	// The retried candidate must end completed in the journal state.
	for _, j := range st.Snapshot() {
		if j.QP == 16 && j.State != store.StateCompleted {
			t.Fatalf("retried job state: %s", j.State)
		}
	}
}

func TestSchedulerFailAfterIsResumable(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir, testParams(4))
	mu := &sync.Mutex{}
	calls := map[int]int{}
	sched := New(st, dir, Config{Workers: 3, FailAfter: 3},
		fakeExecutor(mu, calls, nil))
	_, err := sched.Run(context.Background(), testSpecs(4))
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("want ErrInterrupted, got %v", err)
	}
	st.Close()

	// Reopen: unfinished candidates must be requeued and the run completes.
	st2, err := store.Open(dir, testParams(4))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if st2.Recovered() == 0 {
		t.Fatal("expected recovered candidates after simulated crash")
	}
	sched2 := New(st2, dir, Config{Workers: 2}, fakeExecutor(mu, calls, nil))
	results, err := sched2.Run(context.Background(), testSpecs(4))
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("resume results: %d", len(results))
	}
	for _, j := range st2.Snapshot() {
		if j.State != store.StateCompleted {
			t.Fatalf("job %s left in state %s after resume", j.ID, j.State)
		}
	}
}
