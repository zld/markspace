package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"hlscheck/internal/cut"
	"hlscheck/internal/hls"
	"hlscheck/internal/store"
)

// maxWorkers bounds concurrent profile processing.
const maxWorkers = 4

// job names one input TS file and its profile directory.
type job struct {
	name  string
	input string
}

// sliceAll slices every job into a staging area with bounded
// concurrency; the first failure cancels the rest. Only when all
// profiles succeed are the results published (directory rename), so a
// failed run never leaves half-published output.
func sliceAll(ctx context.Context, jobs []job, root string, target float64) error {
	staging := root + ".staging"
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, maxWorkers)
	errs := make(chan error, len(jobs))
	var wg sync.WaitGroup
	for _, j := range jobs {
		j := j
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			}
			if err := sliceOne(ctx, j, filepath.Join(staging, j.name), target); err != nil {
				errs <- fmt.Errorf("profile %s: %w", j.name, err)
				cancel()
			}
		}()
	}
	wg.Wait()
	close(errs)
	var first error
	for err := range errs {
		if first == nil && err != context.Canceled {
			first = err
		}
	}
	if first != nil {
		os.RemoveAll(staging)
		return first
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	for _, j := range jobs {
		final := filepath.Join(root, j.name)
		if err := os.RemoveAll(final); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(staging, j.name), final); err != nil {
			return err
		}
	}
	return os.RemoveAll(staging)
}

// sliceOne slices one input and writes its manifest last (the manifest
// is the commit point of the profile directory).
func sliceOne(ctx context.Context, j job, outDir string, target float64) error {
	f, err := os.Open(j.input)
	if err != nil {
		return err
	}
	defer f.Close()
	res, err := cut.Slice(ctx, f, outDir, cut.Options{TargetDuration: target})
	if err != nil {
		return err
	}
	for _, a := range res.Anomalies {
		fmt.Fprintf(os.Stderr, "profile %s anomaly: %s\n", j.name, a)
	}
	segs := make([]hls.Segment, len(res.Segments))
	for i, s := range res.Segments {
		segs[i] = hls.Segment{Seq: s.Seq, URI: s.Name, Duration: s.Duration, SHA256: s.SHA256}
	}
	mf, err := store.Create(outDir, "index.m3u8")
	if err != nil {
		return err
	}
	if err := hls.Encode(mf, hls.New(0, segs)); err != nil {
		mf.Abort()
		return err
	}
	return mf.Close()
}
