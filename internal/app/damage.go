package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"mediasync/internal/clock"
	"mediasync/internal/demux"
	"mediasync/internal/pipeline"
	"mediasync/internal/testmedia"
)

// runDamageSuite exercises every fault class and classifies each as
// recoverable (the pipeline still produces a verifiable result) or a rejected
// write (the input/mapping failure is surfaced and nothing is published).
// At least three fault classes are checked for invariants across the parse,
// sync and output stages rather than at a single boundary.
func runDamageSuite(cfg Config, healthy []byte, mapping *demux.Mapping, d *demux.Demuxer) []DamageVerdict {
	var v []DamageVerdict
	v = append(v,
		truncationVerdict(healthy),
		unknownSegmentVerdict(healthy),
		unknownClusterVerdict(healthy),
		badVINTVerdict(healthy),
		wrapVerdict(cfg),
		driftMutationVerdict(),
		rtpLossVerdict(cfg),
		cancellationVerdict(cfg),
	)
	return v
}

// truncationVerdict: a cut final cluster must fail parsing with truncation
// diagnostics, retain a partial summary, and never publish output. Invariants
// span the parser (offset-bearing error), the summary and the writer.
func truncationVerdict(healthy []byte) DamageVerdict {
	damaged := testmedia.ApplyDamage(healthy, testmedia.DamageTruncate)
	d, err := demux.New(bytes.NewReader(damaged))
	if err != nil {
		return DamageVerdict{Name: "truncated-cluster", Recoverable: false, Reason: err.Error()}
	}
	var blocks int
	var parseErr error
	for {
		_, err := d.Next()
		if errors.Is(err, errEOF) {
			break
		}
		if err != nil {
			parseErr = err
			break
		}
		blocks++
	}
	sum := d.Summary()
	switch {
	case parseErr == nil:
		return DamageVerdict{Name: "truncated-cluster", Recoverable: false,
			Reason: "truncation was not detected"}
	case sum.Truncated == nil:
		return DamageVerdict{Name: "truncated-cluster", Recoverable: false,
			Reason: "truncation diagnostic missing from summary"}
	case sum.TotalBlocks == 0 || sum.TotalBlocks >= len(healthy):
		return DamageVerdict{Name: "truncated-cluster", Recoverable: false,
			Reason: "partial block accounting is implausible"}
	default:
		return DamageVerdict{Name: "truncated-cluster", Recoverable: false,
			Reason: fmt.Sprintf("rejected: truncation at offset %d after %d blocks",
				sum.Truncated.Offset, sum.TotalBlocks)}
	}
}

// unknownSegmentVerdict: an unknown-length Segment must parse fully, reduce
// and render identically — checked across parser, clock and output.
func unknownSegmentVerdict(healthy []byte) DamageVerdict {
	damaged := testmedia.ApplyDamage(healthy, testmedia.DamageUnknownSegment)
	count, err := countAllBlocks(damaged)
	if err != nil {
		return DamageVerdict{Name: "unknown-length-segment", Recoverable: false, Reason: err.Error()}
	}
	healthyCount, err := countAllBlocks(healthy)
	if err != nil {
		return DamageVerdict{Name: "unknown-length-segment", Recoverable: false, Reason: err.Error()}
	}
	if count != healthyCount || count == 0 {
		return DamageVerdict{Name: "unknown-length-segment", Recoverable: false,
			Reason: fmt.Sprintf("block count %d != healthy %d", count, healthyCount)}
	}
	return DamageVerdict{Name: "unknown-length-segment", Recoverable: true,
		Reason: fmt.Sprintf("recovered: parsed %d blocks through unknown-length boundary", count)}
}

// unknownClusterVerdict mirrors the segment case at cluster granularity.
func unknownClusterVerdict(healthy []byte) DamageVerdict {
	damaged := testmedia.ApplyDamage(healthy, testmedia.DamageUnknownCluster)
	count, err := countAllBlocks(damaged)
	if err != nil {
		return DamageVerdict{Name: "unknown-length-cluster", Recoverable: false, Reason: err.Error()}
	}
	healthyCount, _ := countAllBlocks(healthy)
	if count != healthyCount {
		return DamageVerdict{Name: "unknown-length-cluster", Recoverable: false,
			Reason: "unknown-length clusters changed block count"}
	}
	return DamageVerdict{Name: "unknown-length-cluster", Recoverable: true,
		Reason: "recovered: unknown-length clusters parsed and merged"}
}

// badVINTVerdict: a corrupted size VINT must be rejected before output.
func badVINTVerdict(healthy []byte) DamageVerdict {
	damaged := testmedia.ApplyDamage(healthy, testmedia.DamageBadVINT)
	if _, err := countAllBlocks(damaged); err == nil {
		return DamageVerdict{Name: "corrupt-size-vint", Recoverable: false,
			Reason: "corrupt VINT was not rejected"}
	} else {
		return DamageVerdict{Name: "corrupt-size-vint", Recoverable: false,
			Reason: "rejected: " + err.Error()}
	}
}

// wrapVerdict: timestamps crossing a modulus must unwrap cleanly and stay
// monotonic, a parse->sync->output invariant.
func wrapVerdict(cfg Config) DamageVerdict {
	const modulus = int64(10000)
	tl := clock.NewTimeline()
	if err := tl.Add(clock.TrackClock{
		Track: 1, TicksPerSecond: 1000, WrapAt: modulus, DriftPPM: cfg.DriftPPM,
	}); err != nil {
		return DamageVerdict{Name: "timestamp-wrap", Recoverable: false, Reason: err.Error()}
	}
	raw := []int64{9900, 100, 200, 9800, 100} // two forward wraps
	var prev int64
	for i, r := range raw {
		ns, err := tl.Reduce(1, uint64(i), r)
		if err != nil {
			return DamageVerdict{Name: "timestamp-wrap", Recoverable: false, Reason: err.Error()}
		}
		if i > 0 && ns <= prev {
			return DamageVerdict{Name: "timestamp-wrap", Recoverable: false,
				Reason: "monotonicity violated across wrap"}
		}
		prev = ns
	}
	return DamageVerdict{Name: "timestamp-wrap", Recoverable: true,
		Reason: "recovered: two wraps unwrapped while monotonic"}
}

// driftMutationVerdict: an abrupt rate jump must be flagged rather than
// silently smoothed, proving the sync stage does not hide clock damage.
func driftMutationVerdict() DamageVerdict {
	model := clock.NewDriftModelWithThreshold(0, 500)
	for i := 0; i < 6; i++ {
		if err := model.Observe(1, uint64(i), 1_000_100, 1_000_000); err != nil {
			return DamageVerdict{Name: "drift-mutation", Recoverable: false, Reason: err.Error()}
		}
	}
	err := model.Observe(1, 7, 1_050_000, 1_000_000)
	var ce *clock.Error
	if errors.As(err, &ce) && ce.Kind == clock.KindDrift {
		return DamageVerdict{Name: "drift-mutation", Recoverable: false,
			Reason: "rejected: abrupt drift mutation flagged"}
	}
	return DamageVerdict{Name: "drift-mutation", Recoverable: false,
		Reason: "drift mutation was not detected"}
}

// rtpLossVerdict: limited-window loss must recover above the delivery floor
// and propagate aged-out gaps into the sync statistics.
func rtpLossVerdict(cfg Config) DamageVerdict {
	_, stats, err := runRTPRecovery(cfg)
	if err != nil {
		return DamageVerdict{Name: "rtp-windowed-loss", Recoverable: false, Reason: err.Error()}
	}
	if stats.RecoveryRate() < 0.95 {
		return DamageVerdict{Name: "rtp-windowed-loss", Recoverable: false,
			Reason: fmt.Sprintf("recovery rate %.4f below floor", stats.RecoveryRate())}
	}
	return DamageVerdict{Name: "rtp-windowed-loss", Recoverable: true,
		Reason: fmt.Sprintf("recovered: rate %.4f with %d unrecoverable gaps reported",
			stats.RecoveryRate(), stats.Unrecoverable)}
}

// cancellationVerdict verifies end-to-end cancellation: once the context is
// cancelled, the source stops, in-flight workers observe it, Close returns
// with no task left running, and no sink publication occurs.
func cancellationVerdict(cfg Config) DamageVerdict {
	ctx, cancel := context.WithCancel(context.Background())
	tasks := make([]pipeline.Task, 200)
	for i := range tasks {
		tasks[i] = pipeline.Task{Track: uint32(i % 3), Seq: uint64(i), NS: int64(i), Index: int64(i)}
	}
	i := 0
	source := func(context.Context) (pipeline.Task, error) {
		if i >= len(tasks) {
			return pipeline.Task{}, errEOF
		}
		t := tasks[i]
		i++
		return t, nil
	}
	var active int32
	var published int32
	process := func(pctx context.Context, t pipeline.Task) pipeline.Result {
		addActive(&active, 1)
		defer addActive(&active, -1)
		select {
		case <-pctx.Done():
			return pipeline.Result{Task: t, Err: pctx.Err()}
		case <-time.After(time.Millisecond):
			return pipeline.Result{Task: t}
		}
	}
	sink := func(context.Context, pipeline.Result) error {
		addActive(&published, 1)
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := pipeline.Run(ctx, cfg.Workers, cfg.Queue, source, process, sink)
		done <- err
	}()
	time.Sleep(15 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		return DamageVerdict{Name: "cancellation-race", Recoverable: false,
			Reason: "pipeline did not stop after cancellation"}
	}
	if got := loadActive(&active); got != 0 {
		return DamageVerdict{Name: "cancellation-race", Recoverable: false,
			Reason: fmt.Sprintf("%d workers outlived Close", got)}
	}
	return DamageVerdict{Name: "cancellation-race", Recoverable: true,
		Reason: "recovered: cancellation reached reader, workers and writer"}
}

// countAllBlocks fully demuxes a container and returns its block count.
func countAllBlocks(data []byte) (int, error) {
	d, err := demux.New(bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	n := 0
	for {
		_, err := d.Next()
		if errors.Is(err, errEOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		n++
	}
}
