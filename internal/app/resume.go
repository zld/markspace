package app

import (
	"os"
	"path/filepath"
	"time"

	"mediatimeline/internal/clock"
	"mediatimeline/internal/container"
	"mediatimeline/internal/pipeline"
)

// RunResume executes the full kill/restart cycle:
//  1. an uninterrupted reference run establishes the canonical output checksum;
//  2. a second run is killed after a deterministic event threshold and its
//     checkpoint + output prefix are durable;
//  3. a resumed pipeline continues from the latest checkpoint;
//  4. the resumed checksum must equal the reference, and recovery time is
//     measured against the sub-500 ms target.
func RunResume(dir string, killAfter time.Duration) (Metrics, int64, uint64, error) {
	spec := fixtureSpec(99)
	path, _, err := writeSource(dir, spec)
	if err != nil {
		return Metrics{}, 0, 0, err
	}
	events, err := scan(path)
	if err != nil {
		return Metrics{}, 0, 0, err
	}

	cfg := pipelineConfig(DemoOptions{Seed: 99, CheckpointEvery: 16})

	// 1. Uninterrupted reference in an isolated directory.
	refDir := filepath.Join(dir, "ref")
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		return Metrics{}, 0, 0, err
	}
	refMetrics, err := runOnce(cfg, refDir, path, events, 0)
	if err != nil {
		return Metrics{}, 0, 0, err
	}

	// 2. Interrupted run with a deterministic crash threshold.
	killN := killThreshold(events, killAfter)
	killDir := filepath.Join(dir, "kill")
	if err := os.MkdirAll(killDir, 0o755); err != nil {
		return Metrics{}, 0, 0, err
	}
	if _, err := runOnce(cfg, killDir, path, events, killN); err != nil &&
		!pipeline.IsKillAfter(err) {
		return Metrics{}, 0, 0, err
	}

	// 3. Resume and measure the recovery window.
	start := time.Now()
	resumed, err := runOnce(cfg, killDir, path, events, 0)
	recoveryMS := time.Since(start).Milliseconds()
	if err != nil {
		return Metrics{}, recoveryMS, 0, err
	}

	// 4. Determinism guard: resumed output must equal the reference.
	if resumed.OutputSHA256 != refMetrics.OutputSHA256 {
		return resumed, recoveryMS, resumed.ReplayedEvents,
			clock.NewError(clock.ErrInvariant,
				"resume checksum mismatch: ref="+refMetrics.OutputSHA256+
					" resumed="+resumed.OutputSHA256)
	}
	return resumed, recoveryMS, resumed.ReplayedEvents, nil
}

// runOnce builds a fresh pipeline over a durable output path and runs one pass.
func runOnce(cfg pipeline.Config, dir, sourcePath string,
	events []container.SampleEvent, kill uint64) (Metrics, error) {
	src, err := os.Open(sourcePath)
	if err != nil {
		return Metrics{}, err
	}
	defer src.Close()
	p, err := pipeline.New(cfg, filepath.Join(dir, "ckpt"),
		filepath.Join(dir, "output.pcm"))
	if err != nil {
		return Metrics{}, err
	}
	p.SetSource(src)
	res, err := p.Run(events, kill)
	if err != nil && !pipeline.IsKillAfter(err) {
		return Metrics{}, err
	}
	return res.Metrics, nil
}

// killThreshold maps the requested duration onto a deterministic event count.
// The kernel is event-driven; the duration only selects how far into the
// fixture the simulated crash occurs, leaving a meaningful prefix to replay.
func killThreshold(events []container.SampleEvent, d time.Duration) uint64 {
	fraction := d.Seconds() / 3.0 // demo baseline is a 3 s kill request
	if fraction <= 0 {
		fraction = 0.25
	}
	if fraction > 0.9 {
		fraction = 0.9
	}
	n := uint64(float64(len(events)) * fraction)
	if n < 1 {
		n = 1
	}
	return n
}
