package batch

import (
	"context"

	"loudscan/internal/media"
	"loudscan/internal/report"
)

// Options configures a batch run.
type Options struct {
	IntegratedMax float64
	TruePeakMax   float64
	Workers       int
	Layout        string
	Probe         media.Options
	Decode        media.DecodeOptions
}

// Runner executes scan tasks.
type Runner struct {
	opts Options
	// probe and decode are seams for tests.
	probe  func(ctx context.Context, path string, opts media.Options) (media.Info, error)
	decode func(ctx context.Context, path string, info media.Info, opts media.DecodeOptions, sink media.Sink) error
}

// New constructs a Runner with the real FFmpeg-backed probe and decoder.
func New(opts Options) *Runner {
	if opts.Workers < 1 {
		opts.Workers = 1
	}
	probeOpts := opts.Probe
	probeOpts.LayoutOverride = opts.Layout
	opts.Probe = probeOpts
	return &Runner{opts: opts, probe: media.Probe, decode: media.Decode}
}

// entryError builds the report entry for a failure at a named stage.
func (r *Runner) entryError(display, stage string, err error) report.Entry {
	return report.Entry{
		Path:          display,
		LoudnessLimit: r.opts.IntegratedMax,
		PeakLimit:     r.opts.TruePeakMax,
		Status:        report.StatusError,
		ErrorStage:    stage,
		Error:         safeError(err),
	}
}

// safeError renders an error for the report. Messages come from our own code
// or from a length-limited tail of FFmpeg's stderr, never from the
// environment, and are truncated so a pathological path cannot flood the
// report.
func safeError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 400 {
		// Cut on a rune boundary so a non-ASCII path cannot produce an
		// invalid UTF-8 report line.
		cut := 400
		for cut > 0 && (msg[cut]&0xC0) == 0x80 {
			cut--
		}
		msg = msg[:cut] + "..."
	}
	return msg
}
