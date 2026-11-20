package batch

import (
	"context"
	"errors"
	"path/filepath"
	"sync"

	"loudscan/internal/ebu"
	"loudscan/internal/media"
	"loudscan/internal/peak"
	"loudscan/internal/report"
)

// Run scans every collected file with bounded concurrency. A failing file
// never cancels the others; every error is folded into its own entry.
func (r *Runner) Run(ctx context.Context, col Collection) ([]report.Entry, error) {
	entries := make([]report.Entry, 0, len(col.Files)+len(col.Errs))
	for _, de := range col.Errs {
		entries = append(entries, r.entryError(filepath.ToSlash(de.Display), de.Stage, de.Err))
	}
	if len(col.Files) == 0 {
		return entries, nil
	}

	// Workers pull items from a channel rather than being spawned per file:
	// concurrency, and therefore peak memory and subprocess count, stays at
	// the --workers ceiling no matter how many files were found.
	work := make(chan int)
	out := make([]report.Entry, len(col.Files))
	var wg sync.WaitGroup
	workers := r.opts.Workers
	if workers > len(col.Files) {
		workers = len(col.Files)
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				out[i] = r.scanOne(ctx, col.Files[i])
			}
		}()
	}

dispatch:
	for i := range col.Files {
		select {
		case work <- i:
		case <-ctx.Done():
			// Stop dispatching; undispatched files get a cancelled entry
			// below so the report still lists every input.
			break dispatch
		}
	}
	close(work)
	wg.Wait()

	for i, e := range out {
		if !e.Reported() {
			out[i] = r.entryError(filepath.ToSlash(col.Files[i].Display), "cancelled", errors.New("cancelled before processing"))
		}
	}
	entries = append(entries, out...)
	return entries, nil
}

// scanOne probes, decodes and measures a single file.
func (r *Runner) scanOne(ctx context.Context, item Item) report.Entry {
	display := filepath.ToSlash(item.Display)
	if err := ctx.Err(); err != nil {
		return r.entryError(display, "cancelled", err)
	}

	info, err := r.probe(ctx, item.Path, r.opts.Probe)
	if err != nil {
		return r.entryError(display, stageOf(err, "probe"), err)
	}
	meter, err := ebu.NewMeter(info.SampleRate, info.Layout)
	if err != nil {
		return r.entryError(display, "loudness", err)
	}
	peakMeter, err := peak.NewMeter(info.Channels)
	if err != nil {
		return r.entryError(display, "peak", err)
	}

	decodeErr := r.decode(ctx, item.Path, info, r.opts.Decode, func(frames []float64) error {
		if err := meter.ProcessInterleaved(frames); err != nil {
			return &stageError{stage: "loudness", err: err}
		}
		if err := peakMeter.Process(frames); err != nil {
			return &stageError{stage: "peak", err: err}
		}
		return nil
	})
	if decodeErr != nil {
		var se *stageError
		if errors.As(decodeErr, &se) {
			return r.entryError(display, se.stage, se.err)
		}
		return r.entryError(display, stageOf(decodeErr, "decode"), decodeErr)
	}
	return r.verdict(display, info, meter.Finish(), peakMeter.Finish())
}

// verdict turns the measurements into a report entry and applies both limits.
func (r *Runner) verdict(display string, info media.Info, loud ebu.Result, truePeak peak.Result) report.Entry {
	entry := report.Entry{
		Path:             display,
		SampleRate:       info.SampleRate,
		Channels:         info.Channels,
		ChannelLayout:    info.Layout.String(),
		IntegratedLUFS:   report.MeasuredLevel(loud.IntegratedLUFS),
		MaxShortTermLUFS: report.MeasuredLevel(loud.MaxShortTermLUFS),
		TruePeakDBTP:     report.MeasuredLevel(truePeak.DBTP),
		LoudnessLimit:    r.opts.IntegratedMax,
		PeakLimit:        r.opts.TruePeakMax,
	}
	if !loud.Measured {
		// Silence and near-silence are a delivery failure, not a pass: the
		// build script must reject a track that carries no programme.
		entry.Status = report.StatusNoMeasurement
		entry.ErrorStage = "measure"
		entry.Error = "no gated loudness blocks above the absolute gate"
		return entry
	}
	loudFail := loud.IntegratedLUFS > r.opts.IntegratedMax
	peakFail := truePeak.DBTP > r.opts.TruePeakMax
	switch {
	case loudFail && peakFail:
		entry.Status = report.StatusBothFail
	case loudFail:
		entry.Status = report.StatusLoudnessFail
	case peakFail:
		entry.Status = report.StatusPeakFail
	default:
		entry.Status = report.StatusPass
	}
	return entry
}

// stageError tags a DSP failure with the module that raised it, so the report
// can say "peak" rather than blaming the decoder.
type stageError struct {
	stage string
	err   error
}

func (e *stageError) Error() string { return e.err.Error() }
func (e *stageError) Unwrap() error { return e.err }

// stageOf picks the stage recorded by a typed error, falling back to the
// caller's guess.
func stageOf(err error, fallback string) string {
	var pe *media.ProbeError
	if errors.As(err, &pe) && pe.Stage != "" {
		return pe.Stage
	}
	var de *media.DecodeError
	if errors.As(err, &de) && de.Stage != "" {
		return de.Stage
	}
	var se *stageError
	if errors.As(err, &se) {
		return se.stage
	}
	return fallback
}
