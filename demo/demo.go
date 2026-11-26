package demo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"archivecheck/fixture"
	"archivecheck/metrics"
	"archivecheck/pipe"
	"archivecheck/state"
)

// Options are the terminal demo parameters.
type Options struct {
	Case     string
	Seed     int64
	Queue    int
	Memory   int64
	SlowMS   int
	WorkRoot string
}

// ValidationError means processing worked but an acceptance metric mismatched.
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

// Run generates samples, executes them and writes one JSON result per case.
func Run(ctx context.Context, opts Options, out io.Writer) error {
	if opts.WorkRoot == "" {
		opts.WorkRoot = ".archivecheck-work"
	}
	if err := os.MkdirAll(opts.WorkRoot, 0o755); err != nil {
		return err
	}

	cases := []string{opts.Case}
	if opts.Case == fixture.CaseAll {
		cases = []string{fixture.CaseNormal, fixture.CaseCorrupt, fixture.CaseRecovery}
	}
	for _, name := range cases {
		rep, err := runCase(ctx, opts, name)
		if werr := rep.WriteJSONL(out); werr != nil {
			return werr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func runCase(ctx context.Context, opts Options, name string) (metrics.Report, error) {
	switch name {
	case fixture.CaseNormal:
		return runNormal(ctx, opts)
	case fixture.CaseCorrupt:
		return runCorrupt(ctx, opts)
	case fixture.CaseRecovery:
		return runRecovery(ctx, opts)
	default:
		return metrics.Report{Case: name, OK: false}, fmt.Errorf("unknown case %q", name)
	}
}

func baseConfig(opts Options, dir string) state.Config {
	return state.Config{
		WorkDir: dir, Queue: opts.Queue, MemoryBytes: opts.Memory, SlowMS: opts.SlowMS,
	}
}

func materialize(opts Options, name, dir string) error {
	f := fixture.Build(name, opts.Seed)
	_, _, _, err := f.Write(dir)
	return err
}

func runNormal(ctx context.Context, opts Options) (metrics.Report, error) {
	dir := filepath.Join(opts.WorkRoot, "normal")
	if err := resetDir(dir); err != nil {
		return metrics.Report{Case: "normal", OK: false}, err
	}
	if err := materialize(opts, fixture.CaseNormal, dir); err != nil {
		return metrics.Report{Case: "normal", OK: false}, err
	}
	res, err := state.Run(ctx, baseConfig(opts, dir))
	if err != nil {
		return metrics.Report{Case: "normal", OK: false, Error: err.Error()}, err
	}
	r := res.Report
	r.Case = "normal"
	if err := checkCommon(r, opts.Memory); err != nil {
		r.OK = false
		r.Error = err.Error()
		return r, &ValidationError{Err: err}
	}
	if r.SyncMS > 20 || r.Loss != 4 || r.Recovered != 4 || r.RecoverRatePct != 100 || r.Duplicate != 2 {
		err := fmt.Errorf("normal metrics mismatch: sync=%.3f loss=%d recovered=%d rate=%s duplicate=%d",
			r.SyncMS, r.Loss, r.Recovered, r.RecoverRate, r.Duplicate)
		r.OK = false
		r.Error = err.Error()
		return r, &ValidationError{Err: err}
	}
	r.OK = true
	return r, nil
}

func runCorrupt(ctx context.Context, opts Options) (metrics.Report, error) {
	dir := filepath.Join(opts.WorkRoot, "corrupt")
	if err := resetDir(dir); err != nil {
		return metrics.Report{Case: "corrupt", OK: false}, err
	}
	if err := materialize(opts, fixture.CaseCorrupt, dir); err != nil {
		return metrics.Report{Case: "corrupt", OK: false}, err
	}
	res, err := state.Run(ctx, baseConfig(opts, dir))
	if err != nil {
		return metrics.Report{Case: "corrupt", OK: false, Error: err.Error()}, err
	}
	r := res.Report
	r.Case = "corrupt"
	if err := checkCommon(r, opts.Memory); err != nil {
		r.OK = false
		r.Error = err.Error()
		return r, &ValidationError{Err: err}
	}
	if r.BadSegments != 2 || r.Fallback != 1 || r.Unresolved != 1 {
		err := fmt.Errorf("corrupt metrics mismatch: bad=%d fallback=%d unresolved=%d",
			r.BadSegments, r.Fallback, r.Unresolved)
		r.OK = false
		r.Error = err.Error()
		return r, &ValidationError{Err: err}
	}
	r.OK = true
	return r, nil
}

func runRecovery(ctx context.Context, opts Options) (metrics.Report, error) {
	continuousDir := filepath.Join(opts.WorkRoot, "recovery-continuous")
	haltDir := filepath.Join(opts.WorkRoot, "recovery-interrupted")
	if err := resetDir(continuousDir); err != nil {
		return metrics.Report{Case: "recovery", OK: false}, err
	}
	if err := resetDir(haltDir); err != nil {
		return metrics.Report{Case: "recovery", OK: false}, err
	}
	if err := materialize(opts, fixture.CaseRecovery, continuousDir); err != nil {
		return metrics.Report{Case: "recovery", OK: false}, err
	}
	if err := materialize(opts, fixture.CaseRecovery, haltDir); err != nil {
		return metrics.Report{Case: "recovery", OK: false}, err
	}

	continuous, err := state.Run(ctx, baseConfig(opts, continuousDir))
	if err != nil {
		return metrics.Report{Case: "recovery", OK: false, Error: err.Error()}, err
	}

	haltCfg := baseConfig(opts, haltDir)
	haltCfg.InterruptCursor = 1
	if _, err := state.Run(ctx, haltCfg); err != nil && !errors.Is(err, pipe.ErrHalt) {
		r := metrics.Report{Case: "recovery", OK: false, Error: err.Error()}
		return r, err
	}

	resumed, err := state.Run(ctx, baseConfig(opts, haltDir))
	if err != nil {
		r := metrics.Report{Case: "recovery", OK: false, Error: err.Error()}
		return r, err
	}
	r := resumed.Report
	r.Case = "recovery"
	r.ContinuousSHA256 = continuous.ArchiveSHA256
	r.ArchiveMatch = resumed.ArchiveSHA256 == continuous.ArchiveSHA256
	if continuous.PeakInFlightBytes > r.InFlightPeakBytes {
		r.InFlightPeakBytes = continuous.PeakInFlightBytes
	}
	if err := checkCommon(r, opts.Memory); err != nil {
		r.OK = false
		r.Error = err.Error()
		return r, &ValidationError{Err: err}
	}
	if r.Resumed != 1 || !r.ArchiveMatch || r.ArchiveSHA256 == "" {
		err := fmt.Errorf("recovery mismatch: resumed=%d match=%v archive=%s continuous=%s",
			r.Resumed, r.ArchiveMatch, r.ArchiveSHA256, continuous.ArchiveSHA256)
		r.OK = false
		r.Error = err.Error()
		return r, &ValidationError{Err: err}
	}
	r.OK = true
	return r, nil
}

func checkCommon(r metrics.Report, memoryLimit int64) error {
	if r.P95MS > 1000 {
		return fmt.Errorf("p95_ms=%.3f exceeds 1000", r.P95MS)
	}
	if memoryLimit > 0 && r.InFlightPeakBytes > memoryLimit {
		return fmt.Errorf("inflight_peak_bytes=%d exceeds %d", r.InFlightPeakBytes, memoryLimit)
	}
	return nil
}

func resetDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o755)
}
