package app

import (
	"os"
	"path/filepath"

	"mediatimeline/internal/container"
	"mediatimeline/internal/pipeline"
)

// media constants shared by every generated scenario.
const (
	inRate       = 44100
	outRate      = 48000
	channels     = 2
	sampleCount  = 240
	framesPerSmp = 512
	targetDBFS   = -20.0
	ceiling      = 0.95
	windowFrames = 1024
)

// fixtureSpec builds the standard synthetic media description, enabling wrap,
// drift and VFR so every clock feature is exercised in one deterministic run.
func fixtureSpec(seed int64) container.FixtureSpec {
	return container.FixtureSpec{
		Seed:          seed,
		Timescale:     inRate,
		SampleCount:   sampleCount,
		FramesPerSamp: framesPerSmp,
		Channels:      channels,
		Wrap32:        true,
		DriftPPM:      420,
		VFRJitter:     10,
	}
}

func pipelineConfig(opts DemoOptions) pipeline.Config {
	return pipeline.Config{
		Seed:            opts.Seed,
		LateMS:          opts.LateMS,
		CheckpointEvery: opts.CheckpointEvery,
		InRate:          inRate,
		OutRate:         outRate,
		Channels:        channels,
		TargetDBFS:      targetDBFS,
		Ceiling:         ceiling,
		WindowFrames:    windowFrames,
	}
}

// writeSource renders a fixture MP4 to dir/source.m4a and returns its path.
func writeSource(dir string, spec container.FixtureSpec) (string, []byte, error) {
	data, err := container.BuildMP4(spec)
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, "source.m4a")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", nil, err
	}
	return path, data, nil
}

// scan opens a container and returns its single audio track's events.
func scan(path string) ([]container.SampleEvent, error) {
	sc, err := container.Open(path)
	if err != nil {
		return nil, err
	}
	defer sc.Close()
	tracks, err := sc.ScanTracks()
	if err != nil {
		return nil, err
	}
	return tracks[0].Entries, nil
}

// RunDemo builds a VFR/drift/wrap fixture, injects late samples and processes
// the stream end to end, returning deterministic metrics.
func RunDemo(dir string, opts DemoOptions) (Metrics, error) {
	spec := fixtureSpec(opts.Seed)
	path, _, err := writeSource(dir, spec)
	if err != nil {
		return Metrics{}, err
	}
	events, err := scan(path)
	if err != nil {
		return Metrics{}, err
	}
	injectLate(events, opts.LateMS, inRate)

	src, err := os.Open(path)
	if err != nil {
		return Metrics{}, err
	}
	defer src.Close()

	cfg := pipelineConfig(opts)
	p, err := pipeline.New(cfg, filepath.Join(dir, "ckpt"), filepath.Join(dir, "output.pcm"))
	if err != nil {
		return Metrics{}, err
	}
	p.SetSource(src)
	res, err := p.Run(events, 0)
	if err != nil {
		return Metrics{}, err
	}
	return res.Metrics, nil
}

// injectLate rewrites a deterministic subset of samples to earlier media time
// and reorders their delivery so they arrive behind the watermark. The late
// window is derived from late-ms on the track timescale.
func injectLate(events []container.SampleEvent, lateMS, timescale int) {
	if lateMS <= 0 || len(events) < 16 {
		return
	}
	shift := uint64(timescale) * uint64(lateMS) / 1000
	lateIdx := []int{len(events) / 4, len(events) / 2, 3 * len(events) / 4}
	for _, idx := range lateIdx {
		if events[idx].DTSNanos > shift {
			events[idx].DTSNanos -= shift
			events[idx].PTSNanos = events[idx].DTSNanos
		}
	}
	// Move the back-dated samples to the tail of the delivery order.
	lateSet := map[int]bool{}
	for _, idx := range lateIdx {
		lateSet[idx] = true
	}
	var onTime, late []container.SampleEvent
	for i, e := range events {
		if lateSet[i] {
			late = append(late, e)
		} else {
			onTime = append(onTime, e)
		}
	}
	copy(events, append(onTime, late...))
}
