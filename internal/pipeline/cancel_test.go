package pipeline

import (
	"context"
	"fmt"
	"testing"
)

func TestCancelAfterTwoLeavesTerminalBatchAndResumeMatchesFullRun(t *testing.T) {
	const n = 12
	gates := make([]chan struct{}, n)
	jobs := make([]Job, n)
	for i := range jobs {
		gates[i] = make(chan struct{})
		i := i
		jobs[i] = Job{
			Index: i, Input: fmt.Sprintf("input-%02d", i), Sample: 1000 + i,
			Work: func(ctx context.Context) (Outcome, error) {
				if i < 2 {
					return Outcome{Output: fmt.Sprintf("output-%02d", i), ByteDelta: 0}, nil
				}
				select {
				case <-ctx.Done():
					return Outcome{}, ctx.Err()
				case <-gates[i]:
					return Outcome{Output: fmt.Sprintf("output-%02d", i), ByteDelta: 0}, nil
				}
			},
		}
	}

	cancelled := Run(context.Background(), jobs, Options{
		Workers: 3, QueueCapacity: 2, CancelAfterCompleted: 2,
	})
	counts := Count(cancelled)
	if counts.Completed != 2 {
		t.Fatalf("completed=%d, want exactly 2: %+v", counts.Completed, counts)
	}
	if counts.Canceled == 0 {
		t.Fatal("expected at least one queued/running job to be canceled")
	}
	if counts.NotStarted == 0 {
		t.Fatal("expected at least one job to remain not started")
	}
	if counts.Completed+counts.Canceled+counts.NotStarted != n {
		t.Fatalf("categories do not sum to %d: %+v", n, counts)
	}
	if err := VerifyBatch(cancelled, n); err != nil {
		t.Fatal(err)
	}

	for _, gate := range gates {
		close(gate)
	}
	rerunJobs := RerunJobs(jobs, cancelled)
	rerun := Run(context.Background(), rerunJobs, Options{Workers: 1, QueueCapacity: 1})
	merged := MergeResume(cancelled, rerun)
	full := Run(context.Background(), jobs, Options{Workers: 3, QueueCapacity: 2})

	if err := VerifyBatch(merged, n); err != nil {
		t.Fatal(err)
	}
	for i := range full {
		if merged[i].Index != full[i].Index || merged[i].Status != StatusSuccess ||
			merged[i].Output != full[i].Output || merged[i].ByteDelta != full[i].ByteDelta {
			t.Fatalf("resume/full mismatch at %d:\nresume=%+v\nfull=%+v", i, merged[i], full[i])
		}
	}
}

func TestPreCancelledContextMarksEveryJobNotStarted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	jobs := []Job{{Index: 0}, {Index: 1}}
	results := Run(ctx, jobs, Options{Workers: 1, QueueCapacity: 1})
	counts := Count(results)
	if counts.NotStarted != 2 || counts.Completed != 0 {
		t.Fatalf("counts = %+v", counts)
	}
}
