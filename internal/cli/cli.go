// Package cli parses rateqa subcommand arguments and validates them before any
// media or storage work happens. Exit codes follow the spec:
//
//	0  complete, 2 bad parameters, 3 media/encode failure,
//	4 storage failure, 75 resumable interruption.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"rateqa/internal/jobs"
)

// Exit codes.
const (
	ExitOK          = 0
	ExitUsage       = 2
	ExitMedia       = 3
	ExitStorage     = 4
	ExitInterrupted = 75
)

// Subcommand names.
const (
	CmdDemo   = "demo"
	CmdRun    = "run"
	CmdResume = "resume"
	CmdReport = "report"
)

// Options holds parsed arguments common to the run-style subcommands.
type Options struct {
	Command          string
	Workspace        string
	Input            string // run only
	Segments         int    // demo only
	FramesPerSegment int
	TargetKbps       int
	Workers          int
	FailAfter        int
}

// ErrUsage wraps parameter problems so main maps them to exit code 2.
var ErrUsage = errors.New("usage error")

// Parse interprets os.Args (without the program name).
func Parse(args []string, stderr io.Writer) (Options, error) {
	if len(args) == 0 {
		return Options{}, usageErr(stderr, "missing subcommand (demo|run|resume|report)")
	}
	cmd := args[0]
	switch cmd {
	case CmdDemo, CmdRun, CmdResume, CmdReport:
	default:
		return Options{}, usageErr(stderr, "unknown subcommand %q", cmd)
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	o := Options{Command: cmd, Workers: 1, FramesPerSegment: 120,
		Segments: 4, TargetKbps: 600, FailAfter: 0}
	fs.StringVar(&o.Workspace, "workspace", "", "working directory for segments, journal and reports")
	fs.StringVar(&o.Input, "input", "", "input Y4M file (run only)")
	fs.IntVar(&o.Segments, "segments", o.Segments, "number of demo segments (demo only)")
	fs.IntVar(&o.FramesPerSegment, "frames-per-segment", o.FramesPerSegment, "frames per segment")
	fs.IntVar(&o.TargetKbps, "target-kbps", o.TargetKbps, "target bitrate in kbps")
	fs.IntVar(&o.Workers, "workers", o.Workers, "worker threads (1-4)")
	fs.IntVar(&o.FailAfter, "fail-after", o.FailAfter, "stop after N completed candidates (demo/run)")
	if err := fs.Parse(args[1:]); err != nil {
		return Options{}, fmt.Errorf("%w: %v", ErrUsage, err)
	}
	if err := o.validate(); err != nil {
		return Options{}, usageErr(stderr, "%v", err)
	}
	return o, nil
}

func (o Options) validate() error {
	if o.Workspace == "" {
		return errors.New("--workspace is required")
	}
	if o.Workers < 1 || o.Workers > jobs.MaxWorkers {
		return fmt.Errorf("--workers must be in [1,%d], got %d", jobs.MaxWorkers, o.Workers)
	}
	if o.FramesPerSegment <= 0 {
		return errors.New("--frames-per-segment must be positive")
	}
	if o.TargetKbps <= 0 {
		return errors.New("--target-kbps must be positive")
	}
	if o.FailAfter < 0 {
		return errors.New("--fail-after must be zero or positive")
	}
	switch o.Command {
	case CmdDemo:
		if o.Segments <= 0 {
			return errors.New("--segments must be positive")
		}
	case CmdRun:
		if o.Input == "" {
			return errors.New("--input is required for run")
		}
	case CmdResume, CmdReport:
		if o.Input != "" {
			return fmt.Errorf("--input is not valid for %s", o.Command)
		}
	}
	return nil
}

func usageErr(stderr io.Writer, format string, a ...any) error {
	fmt.Fprintf(stderr, "rateqa: "+format+"\n", a...)
	fmt.Fprintln(stderr, "usage: rateqa {demo|run|resume|report} [flags]")
	return fmt.Errorf("%w: %s", ErrUsage, fmt.Sprintf(format, a...))
}
