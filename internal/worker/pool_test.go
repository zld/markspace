package worker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"testing"

	"medialab/internal/model"
)

func jobsFor(n int, track model.TrackID, fn func(int) ([]byte, error)) []Job {
	jobs := make([]Job, n)
	for i := 0; i < n; i++ {
		i := i
		jobs[i] = Job{
			Track: track, Ordinal: i, Key: fmt.Sprintf("k%d", i), Size: 16,
			Run: func(ctx context.Context) ([]byte, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				return fn(i)
			},
		}
	}
	return jobs
}

func TestRunCompletesAll(t *testing.T) {
	var running atomic.Int32
	var peak atomic.Int32
	jobs := jobsFor(40, 1, func(i int) ([]byte, error) {
		cur := running.Add(1)
		for {
			old := peak.Load()
			if cur <= old || peak.CompareAndSwap(old, cur) {
				break
			}
		}
		defer running.Add(-1)
		return []byte{byte(i)}, nil
	})
	rep := Run(context.Background(), Config{Workers: 4, MaxCacheBytes: 1 << 20}, jobs)
	if rep.Completed != 40 || len(rep.Faults) != 0 {
		t.Fatalf("completed=%d faults=%v", rep.Completed, rep.Faults)
	}
	if got := peak.Load(); got > 4 {
		t.Fatalf("concurrency %d exceeded cap 4", got)
	}
	if rep.PeakWorkers > 4 {
		t.Fatalf("reported inflight %d", rep.PeakWorkers)
	}
	for i, o := range rep.Outcomes {
		if o.Job.Ordinal != i {
			t.Fatalf("outcome %d ordinal %d", i, o.Job.Ordinal)
		}
	}
}

func TestRunCacheBound(t *testing.T) {
	jobs := make([]Job, 8)
	for i := range jobs {
		jobs[i] = Job{Track: 1, Ordinal: i, Key: fmt.Sprint(i), Size: 100,
			Run: func(ctx context.Context) ([]byte, error) { return []byte{1}, nil }}
	}
	rep := Run(context.Background(), Config{Workers: 8, MaxCacheBytes: 350}, jobs)
	if rep.PeakCache > 350 {
		t.Fatalf("peak cache %d exceeds 350", rep.PeakCache)
	}
	if rep.Completed != 8 {
		t.Fatalf("completed = %d", rep.Completed)
	}
}

func TestRunOversizeJobCancelsTrack(t *testing.T) {
	jobs := []Job{
		{Track: 1, Ordinal: 0, Key: "a", Size: 16, Run: func(context.Context) ([]byte, error) { return []byte{1}, nil }},
		{Track: 1, Ordinal: 1, Key: "b", Size: 5000, Run: func(context.Context) ([]byte, error) { return []byte{2}, nil }},
		{Track: 2, Ordinal: 2, Key: "c", Size: 16, Run: func(context.Context) ([]byte, error) { return []byte{3}, nil }},
	}
	rep := Run(context.Background(), Config{Workers: 2, MaxCacheBytes: 1000}, jobs)
	if rep.TrackCancelled[1] == nil || rep.TrackCancelled[1].Code != model.CodeCacheOverflow {
		t.Fatalf("track 1 not cancelled: %v", rep.TrackCancelled)
	}
	if _, err := findOutcome(rep, 2); err != nil {
		t.Fatalf("track 2 job missing: %v", err)
	}
}

func findOutcome(rep *Report, ordinal int) (Outcome, error) {
	for _, o := range rep.Outcomes {
		if o.Job.Ordinal == ordinal {
			return o, nil
		}
	}
	return Outcome{}, errors.New("not found")
}

func TestRunScopesHardErrorToTrack(t *testing.T) {
	jobs := jobsFor(6, 1, func(i int) ([]byte, error) {
		if i == 2 {
			return nil, model.NewFault(model.KindRender, model.CodePayloadCorrupt, 0, 0, "bad frame")
		}
		return []byte{byte(i)}, nil
	})
	jobs = append(jobs, jobsFor(3, 2, func(i int) ([]byte, error) { return []byte{byte(i)}, nil })...)
	for i := range jobs[6:] {
		jobs[6+i].Ordinal = 6 + i
	}
	rep := Run(context.Background(), Config{Workers: 1, MaxCacheBytes: 1 << 20}, jobs)
	if len(rep.Faults) == 0 {
		t.Fatal("no faults recorded")
	}
	hard := rep.Faults.Hard()
	if len(hard) != 1 || hard[0].Track != 1 {
		t.Fatalf("hard faults = %v", hard)
	}
	for _, o := range rep.Outcomes {
		if o.Job.Track == 2 && o.Fault != nil {
			t.Fatalf("track 2 job failed: %+v", o)
		}
	}
	if rep.Skipped == 0 {
		t.Fatal("remaining track-1 jobs were not skipped")
	}
}

func TestRunRecoverableErrorDoesNotCancel(t *testing.T) {
	jobs := jobsFor(4, 1, func(i int) ([]byte, error) {
		if i == 1 {
			return nil, model.NewFault(model.KindRender, model.CodeFrameMissing, 1, 1, "gap").RecoverableFault()
		}
		return []byte{byte(i)}, nil
	})
	rep := Run(context.Background(), Config{Workers: 2, MaxCacheBytes: 1 << 20}, jobs)
	if rep.Completed != 3 {
		t.Fatalf("completed = %d", rep.Completed)
	}
	if len(rep.TrackCancelled) != 0 {
		t.Fatalf("recoverable error cancelled track: %v", rep.TrackCancelled)
	}
	if rep.Faults.HasHard() {
		t.Fatalf("recoverable fault classified hard: %v", rep.Faults)
	}
}

func TestRunContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	jobs := jobsFor(4, 1, func(i int) ([]byte, error) { return []byte{byte(i)}, nil })
	rep := Run(ctx, Config{Workers: 2, MaxCacheBytes: 1 << 20}, jobs)
	if rep.Completed != 0 {
		t.Fatalf("cancelled run completed %d jobs", rep.Completed)
	}
}

func TestRunDeterministicOrder(t *testing.T) {
	jobs := jobsFor(20, 1, func(i int) ([]byte, error) { return []byte{byte(i)}, nil })
	rep := Run(context.Background(), Config{Workers: 5, MaxCacheBytes: 1 << 20}, jobs)
	seq := []int{}
	for _, o := range rep.Outcomes {
		seq = append(seq, o.Job.Ordinal)
	}
	if !sort.IntsAreSorted(seq) {
		t.Fatalf("outcome order = %v", seq)
	}
}
