package main

import (
	"sync"

	"assetcheck/internal/fixture"
	"assetcheck/internal/task"
)

func demoInput(path, assetID string, crash task.Stage) task.Input {
	return task.Input{
		SourcePath:        path,
		AssetID:           assetID,
		RequiredLanguages: fixture.RequiredLanguages(),
		ExpectedCues:      fixture.ExpectedCues(fixture.DefaultCues()),
		CrashAfter:        crash,
	}
}

func deliverConcurrent(workDir string, in task.Input, count int) ([]*task.Result, error) {
	if count < 1 {
		count = 1
	}
	results := make([]*task.Result, count)
	errs := make([]error, count)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = task.NewRunner(workDir).Process(in)
		}(i)
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

func sameManifestSHA(results []*task.Result) bool {
	if len(results) == 0 || results[0] == nil {
		return false
	}
	want := results[0].ManifestSHA256
	for _, r := range results[1:] {
		if r == nil || r.ManifestSHA256 != want {
			return false
		}
	}
	return true
}

func committedResult(results []*task.Result) *task.Result {
	for _, r := range results {
		if r != nil && r.Committed {
			return r
		}
	}
	for _, r := range results {
		if r != nil && r.IndexRebuilt {
			return r
		}
	}
	if len(results) > 0 {
		return results[0]
	}
	return nil
}

func rebuiltResult(results []*task.Result) *task.Result {
	for _, r := range results {
		if r != nil && r.IndexRebuilt {
			return r
		}
	}
	return committedResult(results)
}

func countCommitted(results []*task.Result) int {
	n := 0
	for _, r := range results {
		if r != nil && r.Committed {
			n++
		}
	}
	return n
}
