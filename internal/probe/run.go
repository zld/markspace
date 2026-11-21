package probe

import (
	"context"
	"sync"

	"streamcheck/internal/manifest"
)

// Run probes every segment in place, filling DurationMS or ProbeError.
// Segments are probed concurrently, up to Options.Concurrency at a time, and
// every segment is attempted even when earlier ones fail.
func Run(ctx context.Context, segments []manifest.Segment, opts Options) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > len(segments) {
		concurrency = len(segments)
	}
	if concurrency < 1 {
		return
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				ms, err := Duration(ctx, opts.Binary, segments[i].Path, timeout)
				if err != nil {
					segments[i].DurationMS = 0
					segments[i].ProbeError = err.Error()
					continue
				}
				segments[i].DurationMS = ms
				segments[i].ProbeError = ""
			}
		}()
	}
	for i := range segments {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}
