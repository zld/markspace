package pipeline

import (
	"context"
	"fmt"
	"testing"

	"archivecheck/internal/diagnostic"
)

func TestRunMergesInInputOrderRegardlessOfCompletion(t *testing.T) {
	const n = 3
	started := make(chan int, n)
	release := make([]chan struct{}, n)
	for i := range release {
		release[i] = make(chan struct{})
	}
	jobs := make([]Job, n)
	for i := 0; i < n; i++ {
		i := i
		jobs[i] = Job{
			Index: i, Input: fmt.Sprintf("in-%d", i), Sample: 100 + i,
			Work: func(context.Context) (Outcome, error) {
				started <- i
				<-release[i]
				return Outcome{Output: fmt.Sprintf("out-%d", i), TimeDeltaNS: 0, ByteDelta: 0}, nil
			},
		}
	}

	done := make(chan []Result, 1)
	go func() {
		done <- Run(context.Background(), jobs, Options{Workers: 3, QueueCapacity: 1})
	}()

	for i := 0; i < n; i++ {
		<-started
	}
	for _, i := range []int{2, 1, 0} {
		close(release[i])
	}
	results := <-done

	if err := VerifyBatch(results, n); err != nil {
		t.Fatal(err)
	}
	for i, r := range results {
		if r.Index != i || r.Output != fmt.Sprintf("out-%d", i) || r.Status != StatusSuccess {
			t.Fatalf("result %d = %+v", i, r)
		}
	}
}

func TestRunFailuresAreIsolated(t *testing.T) {
	jobs := []Job{
		{Index: 0, Sample: 20, Work: func(context.Context) (Outcome, error) { return Outcome{Output: "a"}, nil }},
		{Index: 1, Sample: 21, Work: func(context.Context) (Outcome, error) {
			return Outcome{}, diagnostic.New(diagnostic.CodeLineTooLarge, diagnostic.PhaseSubtitle, 21, 55, "too big")
		}},
		{Index: 2, Sample: 22, Work: func(context.Context) (Outcome, error) { return Outcome{Output: "c"}, nil }},
	}
	results := Run(context.Background(), jobs, Options{Workers: 2, QueueCapacity: 1})
	counts := Count(results)
	if counts.Success != 2 || counts.Failed != 1 || counts.Completed != 3 {
		t.Fatalf("counts = %+v", counts)
	}
	if results[1].Diagnostic == nil || results[1].Diagnostic.Offset != 55 ||
		results[1].Diagnostic.Code != diagnostic.CodeLineTooLarge {
		t.Fatalf("failure = %+v", results[1].Diagnostic)
	}
	if results[0].Output != "a" || results[2].Output != "c" {
		t.Fatal("successful samples did not continue")
	}
}

func TestOptionsAreBounded(t *testing.T) {
	got := Options{Workers: 999, QueueCapacity: 999}.Normalize()
	if got.Workers != MaxWorkers || got.QueueCapacity != MaxQueue {
		t.Fatalf("normalized = %+v", got)
	}
	got = Options{}.Normalize()
	if got.Workers != defaultWorkers || got.QueueCapacity != defaultWorkers*2 {
		t.Fatalf("default = %+v", got)
	}
}
