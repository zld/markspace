package demo

import (
	"context"
	"errors"
	"time"

	"mediapipeline/internal/budget"
	"mediapipeline/internal/check"
	"mediapipeline/internal/checkpoint"
	"mediapipeline/internal/fault"
	"mediapipeline/internal/fixture"
	"mediapipeline/internal/ingest"
	"mediapipeline/internal/merge"
	"mediapipeline/internal/transport"
	"mediapipeline/internal/worker"
)

func validateConfig(cfg Config) error {
	switch cfg.Codec {
	case ingest.CodecH264, ingest.CodecH265, ingest.CodecAV1:
	default:
		return errors.New("demo: unknown codec " + string(cfg.Codec))
	}
	if cfg.StateDir == "" {
		return errors.New("demo: empty state directory")
	}
	if cfg.InterruptAfter < 0 {
		return errors.New("demo: negative interrupt-after")
	}
	return nil
}

// runPipeline is the shared path for clean, damaged and resuming runs.
func runPipeline(ctx context.Context, cfg Config, sample *fixture.Sample) (*Result, error) {
	start := time.Now()
	res := &Result{InputDamage: len(cfg.Injections) > 0}
	var report fault.Report

	// --- transport: dedup, reorder, wrap expansion, gap recording -----------
	ro, err := transport.NewReorder(&report)
	if err != nil {
		return nil, err
	}
	for arrival, raw := range sample.Packets {
		if err := ro.Push(raw, arrival); err != nil {
			return nil, err
		}
	}
	var ssrc uint32
	if ss := ro.SSRCs(); len(ss) > 0 {
		ssrc = ss[0]
		for _, s := range ss {
			ro.Flush(s)
		}
	}
	gaps := ro.Gaps(ssrc)
	res.Gaps = len(gaps)

	// --- ingest: re-scan the (possibly truncated) stream and index ----------
	units, err := ingest.Scan(sample.Codec, sample.Stream, ingest.DefaultLimits, &report)
	if err != nil {
		return nil, err
	}
	indexFile, err := ingest.ParseIndex(sample.IndexBytes, units, ingest.DefaultLimits, &report)
	if err != nil && !report.Has(fault.CodeIndexOutOfBounds) {
		return nil, err
	}

	// --- budget: plan under current bandwidth, retrying after a drop --------
	planner, err := budget.NewPlanner(budget.DefaultLimits)
	if err != nil {
		return nil, err
	}
	remainingBytes := int64(len(sample.Stream))
	// A bandwidth-drop injection starts the window at the choked rate.
	firstBps := sample.Bandwidth.NormalBps
	if hasInjection(cfg, fixture.InjectBandwidthDrop) {
		firstBps = sample.Bandwidth.DroppedBps
	}
	plan := planner.Plan(budget.Input{
		BandwidthBps:   firstBps,
		RemainingBytes: remainingBytes,
		RemainingUnits: len(units),
		Gaps:           res.Gaps,
	})
	if plan.Rejected {
		res.BudgetRejections++
		if hasInjection(cfg, fixture.InjectBandwidthDrop) {
			// The drop window recovers to the normal bandwidth; re-plan once.
			plan = planner.Plan(budget.Input{
				BandwidthBps:   sample.Bandwidth.NormalBps,
				RemainingBytes: remainingBytes,
				RemainingUnits: len(units),
				Gaps:           res.Gaps,
			})
		}
	}
	if plan.Rejected {
		res.Damage = appendFaultLines(res.Damage, plan.Faults)
		return res, nil
	}

	// --- worker: only budget-admitted tasks, checkpointed and idempotent -----
	tasks := make([]worker.Task, 0, len(units))
	for i, u := range units {
		tasks = append(tasks, worker.Task{
			Key: unitKey(i), UnitIndex: i, Offset: u.Offset,
			Payload: append([]byte(nil), u.Payload...),
		})
	}
	store, err := checkpoint.Open(checkpointPath(cfg), &report)
	if err != nil {
		return nil, err
	}
	before := store.Len()
	pool, err := worker.New(worker.Options{
		Concurrency:    plan.Concurrency,
		InterruptAfter: cfg.InterruptAfter,
		Process:        deterministicProcess,
	}, store)
	if err != nil {
		return nil, err
	}
	summary, err := pool.Run(ctx, tasks)
	if err != nil {
		return nil, err
	}
	res.ElapsedMS = time.Since(start).Milliseconds()
	if res.ElapsedMS < 0 {
		res.ElapsedMS = 0
	}

	// An interrupted first run stops here; resume re-enters with the checkpoint.
	if summary.Interrupted && !cfg.Resume {
		res.InterruptedRun = true
		res.Resumed = false
		res.ResumedUnits = store.Len()
		return res, nil
	}
	res.Resumed = cfg.Resume
	res.ResumedUnits = store.Len() - before

	// --- merge: reconcile the three generated versions ----------------------
	manifest, err := merge.Build(sample.Versions, &report)
	if err != nil {
		return nil, err
	}
	res.Assets = len(manifest.Assets)
	res.Missing = report.CountCode(fault.CodeAssetMissing)
	res.Conflicts = report.CountCode(fault.CodeIndexConflict) +
		report.CountCode(fault.CodeByteConflict)

	stager, err := merge.NewStager(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	if err := stager.Stage(manifest); err != nil {
		return nil, err
	}

	// --- independent validation against raw bytes, never pool markers -------
	staged, err := readStaged(stager)
	if err != nil {
		return nil, err
	}
	verdict := check.Validate(check.Input{
		Codec:    sample.Codec,
		Stream:   sample.Stream,
		Index:    indexFile,
		Manifest: manifest,
		GapSSRC:  ssrc,
		Gaps:     gaps,
		Staged:   staged,
		Limits:   ingest.DefaultLimits,
	})
	// Surface every fault collected before validation plus the verdict faults.
	res.Damage = appendFaultLines(nil, report.All())
	if !verdict.Pass {
		for _, f := range verdict.Faults {
			res.ValidationFaults = append(res.ValidationFaults,
				DamageLine{Offset: f.Offset, Code: string(f.Code), Msg: f.Message})
		}
		reason := joinReasons(verdict.Faults)
		if err := stager.Rollback(manifest, reason); err != nil {
			return nil, err
		}
		res.RolledBack = true
		res.RollbackReason = reason
		res.ValidatorPass = false
		return res, nil
	}
	if err := stager.Commit(manifest); err != nil {
		return nil, err
	}
	res.ValidatorPass = true
	res.SHA256 = verdict.Digest
	return res, nil
}

func hasInjection(cfg Config, name string) bool {
	for _, in := range cfg.Injections {
		if in == name {
			return true
		}
	}
	return false
}

func unitKey(i int) string {
	return keyPrefix + digits(i)
}

const keyPrefix = "u"

// digits renders i as a zero-padded four-digit ordinal, matching the key shape
// used in worker tests so checkpoint records stay human-readable.
func digits(i int) string {
	const width = 4
	var b [width]byte
	for p := width - 1; p >= 0; p-- {
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[:])
}
