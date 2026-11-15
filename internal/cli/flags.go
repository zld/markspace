// Package cli parses terminal arguments into typed options and owns the
// process exit code contract.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"subsync/internal/model"
	"subsync/internal/overlap"
	"subsync/internal/timeline"
)

// Exit codes promised to scripting consumers.
const (
	ExitOK        = 0
	ExitGeneral   = 1
	ExitUsage     = 2
	ExitDecode    = 3
	ExitThreshold = 4
	ExitIO        = 5
)

// Options holds every switch accepted by both the run and demo verbs.
type Options struct {
	Demo bool

	Input        string
	Output       string
	InputFormat  model.Format
	OutputFormat model.Format
	Encoding     string
	FPS          timeline.FPS
	Overlap      overlap.Strategy
	Track        string
	SkipBad      bool
	JSONL        bool
	MaxDeltaMS   int64
	MaxDropped   int
	WorkDir      string
}

// UsageError marks argument problems that map to ExitUsage.
type UsageError struct{ Err error }

func (e *UsageError) Error() string { return e.Err.Error() }

func usageErr(format string, args ...any) error {
	return &UsageError{Err: fmt.Errorf(format, args...)}
}

// Parse interprets os.Args[1:]. The first positional word selects the verb
// ("run" is implied when absent).
func Parse(args []string, stderr io.Writer) (*Options, error) {
	if len(args) == 0 {
		printUsage(stderr)
		return nil, usageErr("missing subcommand (run|demo)")
	}
	demo := false
	switch args[0] {
	case "demo":
		demo = true
		args = args[1:]
	case "run":
		args = args[1:]
	case "-h", "--help", "help":
		printUsage(stderr)
		return nil, usageErr("help requested")
	}

	fs := flag.NewFlagSet("subsync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printUsage(stderr) }

	opts := &Options{Demo: demo}
	var (
		inFmt, outFmt, fpsS, overlapS, workDir string
	)
	fs.StringVar(&opts.Input, "input", "", "input subtitle file path")
	fs.StringVar(&opts.Output, "output", "", "output subtitle file path")
	fs.StringVar(&inFmt, "input-format", "", "srt|vtt|ass")
	fs.StringVar(&outFmt, "output-format", "", "srt|vtt|ass")
	fs.StringVar(&opts.Encoding, "encoding", "", "force encoding (UTF-8|UTF-16LE|UTF-16BE|GB18030|Big5)")
	fs.StringVar(&fpsS, "fps", "25", "frame rate as integer fraction, e.g. 24000/1001")
	fs.StringVar(&overlapS, "overlap", "keep", "overlap policy: keep|clip|fail")
	fs.StringVar(&opts.Track, "track", "default", "track name assigned to the file's cues")
	fs.StringVar(&workDir, "work-dir", "", "directory for demo artifacts (default: temp dir)")
	fs.BoolVar(&opts.SkipBad, "skip-bad", false, "skip malformed cues instead of failing the batch")
	fs.BoolVar(&opts.JSONL, "jsonl", false, "emit JSON Lines report")
	fs.Int64Var(&opts.MaxDeltaMS, "max-delta-ms", 42, "maximum allowed round-trip deviation in ms")
	fs.IntVar(&opts.MaxDropped, "max-dropped", 0, "maximum allowed dropped cues")

	if err := fs.Parse(args); err != nil {
		// flag.ErrHelp (user asked for -h) is still a usage-class exit code.
		return nil, &UsageError{Err: err}
	}

	var err error
	if inFmt == "" {
		return nil, usageErr("--input-format is required")
	}
	if opts.InputFormat, err = model.ParseFormat(inFmt); err != nil {
		return nil, &UsageError{Err: err}
	}
	if outFmt == "" {
		return nil, usageErr("--output-format is required")
	}
	if opts.OutputFormat, err = model.ParseFormat(outFmt); err != nil {
		return nil, &UsageError{Err: err}
	}
	if opts.FPS, err = timeline.ParseFPS(fpsS); err != nil {
		return nil, &UsageError{Err: err}
	}
	if opts.Overlap, err = overlap.ParseStrategy(overlapS); err != nil {
		return nil, &UsageError{Err: err}
	}
	if opts.MaxDeltaMS < 0 {
		return nil, usageErr("--max-delta-ms must be non-negative")
	}
	if opts.MaxDropped < 0 {
		return nil, usageErr("--max-dropped must be non-negative")
	}
	opts.WorkDir = workDir

	if !demo {
		if strings.TrimSpace(opts.Input) == "" {
			return nil, usageErr("--input is required for run")
		}
		if strings.TrimSpace(opts.Output) == "" {
			return nil, usageErr("--output is required for run")
		}
	}
	return opts, nil
}

// IsUsage reports whether an error represents a parameter problem.
func IsUsage(err error) bool {
	var u *UsageError
	return errors.As(err, &u)
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `subsync - offline subtitle normalizer and frame-rate converter

usage:
  subsync run  --input FILE --output FILE --input-format F --output-format F
               [--encoding E] [--fps N/D] [--overlap keep|clip|fail]
               [--track NAME] [--skip-bad] [--jsonl]
               [--max-delta-ms N] [--max-dropped N]
  subsync demo --input-format F --output-format F [same conversion flags]

exit codes: 0 ok | 2 usage | 3 decode/parse | 4 threshold exceeded | 5 io`)
}
