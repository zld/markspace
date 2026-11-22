// Package app is the orchestration core: it wires the EBML/demux, clock, RTP
// recovery, pipeline, remux and report stages into one deterministic run and
// keeps cmd/mediasync a thin argument adapter.
package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"mediasync/internal/clock"
	"mediasync/internal/demux"
	"mediasync/internal/pipeline"
	"mediasync/internal/remux"
	"mediasync/internal/report"
	"mediasync/internal/testmedia"
)

// Config is the validated demo/run configuration.
type Config struct {
	Seed       int64
	Tracks     []string
	DriftPPM   float64
	WrapAt     int64
	Loss       float64
	Workers    int
	Queue      int
	Clusters   int
	PerCluster int
}

// DamageVerdict records how one damaged fixture was classified.
type DamageVerdict struct {
	Name        string
	Recoverable bool
	Reason      string
}

// Result is everything the command layer prints and exits on.
type Result struct {
	Metrics    report.Metrics
	Verdicts   []DamageVerdict
	WebMPath   string
	MP4Path    string
	HealthyErr error
}

// Run executes the full pipeline once, including the damaged-fixture suite,
// and returns quantified metrics. A non-nil error means an internal invariant
// failed (exit 3); expected input damage is captured in verdicts instead.
func Run(ctx context.Context, cfg Config, workDir string) (*Result, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	spec := testmedia.Spec{
		Seed: cfg.Seed, Tracks: cfg.Tracks,
		Clusters: cfg.Clusters, BlocksPerCluster: cfg.PerCluster,
		WrapAt: cfg.WrapAt,
	}
	data, truth := testmedia.BuildWebM(spec)

	// --- Stage 1: demux the healthy container -----------------------------
	in, mapping, samples, inputSummary, err := demuxContainer(data)
	if err != nil {
		return nil, fmt.Errorf("healthy demux failed: %w", err)
	}

	// --- Stage 2: reduce every sample onto the shared nanosecond base -----
	tl := clock.NewTimeline()
	for _, c := range mapping.ClockConfigs(cfg.WrapAt, driftMap(mapping.Sources(), cfg.DriftPPM)) {
		if err := tl.Add(c); err != nil {
			return nil, err
		}
	}
	syncErrs, timed, err := reduceSamples(tl, samples, in.ScaleNS())
	if err != nil {
		return nil, err
	}

	// --- Stage 3: bounded concurrent processing with deterministic merge --
	latencies := make([]float64, 0, len(timed))
	var mu sync.Mutex
	var clocks sync.Map // index -> enqueue time
	process := func(pctx context.Context, t pipeline.Task) pipeline.Result {
		started, _ := clocks.LoadAndDelete(t.Index)
		if t0, ok := started.(time.Time); ok {
			mu.Lock()
			latencies = append(latencies, float64(time.Since(t0).Microseconds())/1000.0)
			mu.Unlock()
		}
		return pipeline.Result{Task: t, Out: append([]byte(nil), t.Data...)}
	}
	taskSource := makeTaskSource(timed, &clocks)
	var merged []pipeline.Result
	sink := func(_ context.Context, r pipeline.Result) error {
		merged = append(merged, r)
		return nil
	}
	if _, err := pipeline.Run(ctx, cfg.Workers, cfg.Queue, taskSource, process, sink); err != nil {
		return nil, fmt.Errorf("pipeline failure: %w", err)
	}
	if len(merged) != len(timed) {
		return nil, fmt.Errorf("%w: pipeline emitted %d of %d",
			report.ErrInvariant, len(merged), len(timed))
	}

	// --- Stage 4: lossless remux, twice, to prove byte-level determinism --
	remuxSamples := toRemuxSamples(merged)
	w, err := remux.NewWriter(mapping, in.Tracks(), in.ScaleNS())
	if err != nil {
		return nil, err
	}
	webmPath := filepath.Join(workDir, "deliverable.webm")
	outcome, err := w.Publish(webmPath, remuxSamples)
	if err != nil || !outcome.Published {
		return nil, fmt.Errorf("remux publish failed: %w", err)
	}
	secondDigest, err := rerunDigest(mapping, in, remuxSamples)
	if err != nil {
		return nil, err
	}
	mp4Path := filepath.Join(workDir, "deliverable.mp4")
	if err := writeVideoSidecar(mp4Path, in, remuxSamples); err != nil {
		return nil, err
	}

	// --- Stage 5: RTP recovery over an impaired stream --------------------
	rcv, rcvStats, err := runRTPRecovery(cfg)
	if err != nil {
		return nil, err
	}
	_ = rcv

	// --- Stage 6: damaged-fixture suite -----------------------------------
	verdicts := runDamageSuite(cfg, data, mapping, in)

	// --- Stage 7: independent reconciliation ------------------------------
	metrics, err := report.Build(report.Options{
		Input: report.InputSummary{
			TimecodeScaleNS: inputSummary.TimecodeScaleNS,
			TrackBlocks:     inputSummary.TrackBlocks,
			TotalBlocks:     inputSummary.TotalBlocks,
			InputBytes:      inputSummary.InputBytes,
			Truncated:       inputSummary.Truncated,
		},
		Output: report.OutputSummary{
			TracksMapped: outcome.Tracks, Samples: outcome.Samples,
			OutputBytes: outcome.Bytes, Clusters: outcome.Clusters,
			PayloadHash: outcome.PayloadHash, Published: outcome.Published,
		},
		Recovery: report.RecoverySummary{
			Expected: rcvStats.Expected, Received: rcvStats.Received,
			Duplicates: rcvStats.Duplicates, Reordered: rcvStats.Reordered,
			Recovered: rcvStats.Recovered, Unrecoverable: rcvStats.Unrecoverable,
		},
		SyncErrorsMS: syncErrs, LatenciesMS: latencies,
		FirstDigest: outcome.PayloadHash, SecondDigest: secondDigest,
		DamagedCases:   len(verdicts),
		Recoverable:    countVerdicts(verdicts, true),
		RejectedWrites: countVerdicts(verdicts, false),
	})
	if err != nil {
		return nil, err
	}
	if len(truth.Blocks) != outcome.Samples {
		return nil, fmt.Errorf("%w: truth blocks %d != published %d",
			report.ErrInvariant, len(truth.Blocks), outcome.Samples)
	}
	return &Result{
		Metrics: metrics, Verdicts: verdicts,
		WebMPath: webmPath, MP4Path: mp4Path,
	}, nil
}

func demuxContainer(data []byte) (*demux.Demuxer, *demux.Mapping, []*demux.Sample, report.InputSummary, error) {
	d, err := demux.New(bytes.NewReader(data))
	if err != nil {
		return nil, nil, nil, report.InputSummary{}, err
	}
	bindings := make([]demux.Binding, 0, len(d.Tracks()))
	for _, tr := range d.Tracks() {
		role, ok := demux.RoleForCodec(tr.CodecID)
		if !ok {
			return nil, nil, nil, report.InputSummary{},
				fmt.Errorf("unsupported codec %q", tr.CodecID)
		}
		bindings = append(bindings, demux.Binding{Source: tr.Number, Role: role})
	}
	mapping, err := demux.Resolve(d.Tracks(), bindings)
	if err != nil {
		return nil, nil, nil, report.InputSummary{}, err
	}
	var samples []*demux.Sample
	trackBlocks := map[uint32]int{}
	for {
		s, err := d.Next()
		if errors.Is(err, errEOF) {
			break
		}
		if err != nil {
			return nil, nil, nil, report.InputSummary{}, err
		}
		samples = append(samples, s)
		trackBlocks[s.Track]++
	}
	sum := d.Summary()
	summary := report.InputSummary{
		TimecodeScaleNS: sum.TimecodeScaleNS,
		TrackBlocks:     trackBlocks,
		TotalBlocks:     sum.TotalBlocks,
		InputBytes:      sum.Bytes,
		Truncated:       sum.Truncated != nil,
		TruncOffset:     truncOffset(sum),
	}
	return d, mapping, samples, summary, nil
}
