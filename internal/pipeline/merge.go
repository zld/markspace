package pipeline

import (
	"fmt"
	"sort"

	"archivecheck/internal/diagnostic"
)

// MergeOrdered deterministically merges results in original input-index order.
// Completion order and worker count never affect the returned order.
func MergeOrdered(results []Result) []Result {
	out := append([]Result(nil), results...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Index == out[j].Index {
			return out[i].Sample < out[j].Sample
		}
		return out[i].Index < out[j].Index
	})
	return out
}

// VerifyBatch ensures exactly one terminal result exists for each index.
func VerifyBatch(results []Result, total int) error {
	seen := make(map[int]int, len(results))
	for _, r := range results {
		seen[r.Index]++
		if r.Index < 0 || r.Index >= total {
			return diagnostic.New(diagnostic.CodeInternal, diagnostic.PhaseMerge, r.Sample, 0,
				"result index %d outside batch [0,%d)", r.Index, total)
		}
	}
	if len(seen) != total || len(results) != total {
		return diagnostic.New(diagnostic.CodeInternal, diagnostic.PhaseMerge, -1, 0,
			"merged %d results covering %d indices, want %d unique indices",
			len(results), len(seen), total)
	}
	for i := 0; i < total; i++ {
		if seen[i] != 1 {
			return diagnostic.New(diagnostic.CodeInternal, diagnostic.PhaseMerge, i, 0,
				"index %d has %d results, want exactly one", i, seen[i])
		}
	}
	return nil
}

// MergeResume combines retained completed results with a rerun of unfinished
// jobs. Rerun results replace unfinished placeholders by index.
func MergeResume(completed []Result, rerun []Result) []Result {
	byIndex := make(map[int]Result)
	for _, r := range completed {
		if r.Status == StatusSuccess || r.Status == StatusFailed {
			byIndex[r.Index] = r
		}
	}
	for _, r := range rerun {
		byIndex[r.Index] = r
	}
	out := make([]Result, 0, len(byIndex))
	for _, r := range byIndex {
		out = append(out, r)
	}
	return MergeOrdered(out)
}

// IndicesToRerun returns canceled/not-started indices in ascending order.
func IndicesToRerun(results []Result) []int {
	var out []int
	for _, r := range results {
		if r.Status == StatusCanceled || r.Status == StatusNotStarted {
			out = append(out, r.Index)
		}
	}
	sort.Ints(out)
	return out
}

// RerunJobs selects jobs whose indices need rerunning.
func RerunJobs(all []Job, results []Result) []Job {
	need := make(map[int]bool)
	for _, idx := range IndicesToRerun(results) {
		need[idx] = true
	}
	var out []Job
	for _, job := range all {
		if need[job.Index] {
			out = append(out, job)
		}
	}
	return out
}

// DescribeOrder is a helper used by tests and reports.
func DescribeOrder(results []Result) string {
	return fmt.Sprintf("%v", indexes(results))
}

func indexes(results []Result) []int {
	out := make([]int, 0, len(results))
	for _, r := range results {
		out = append(out, r.Index)
	}
	return out
}
