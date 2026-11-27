// Command continuity is the thin terminal adapter over the reusable engine.
// It only prepares self-generated fixtures, drives one (or a resumed) run and
// prints an independently checkable summary; all processing logic lives in the
// internal packages.
package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"continuity/internal/engine"
	"continuity/internal/fixture"
	"continuity/internal/model"
)

const (
	exitOK      = 0
	exitFailure = 2
	exitUsage   = 64
)

func main() {
	os.Exit(run(os.Args[1:]))
}

type options struct {
	workspace      string
	frames         int
	interruptAfter int
	resume         bool
	inject         string
	mutate         string
	threads        int
	maxBytes       int64
	lufsTolerance  float64
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return exitUsage
	}
	if args[0] != "demo" {
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		printUsage()
		return exitUsage
	}
	opts, err := parseFlags(args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", err)
		return exitUsage
	}
	if err := execute(opts); err != nil {
		printError(err)
		var se *model.StructuredError
		if errors.As(err, &se) {
			return exitFailure
		}
		if errors.Is(err, engine.ErrInterrupted) {
			return exitOK
		}
		return exitFailure
	}
	return exitOK
}

func parseFlags(args []string) (options, error) {
	opts := options{
		frames:        10,
		threads:       runtime.NumCPU(),
		maxBytes:      32 << 20,
		lufsTolerance: 1.0,
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		need := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s requires a value", a)
			}
			i++
			return args[i], nil
		}
		switch a {
		case "--workspace":
			v, err := need()
			if err != nil {
				return opts, err
			}
			opts.workspace = v
		case "--frames":
			v, err := need()
			if err != nil {
				return opts, err
			}
			n, perr := parseInt(v)
			if perr != nil || n < 1 {
				return opts, fmt.Errorf("--frames must be a positive integer")
			}
			opts.frames = n
		case "--interrupt-after":
			v, err := need()
			if err != nil {
				return opts, err
			}
			n, perr := parseInt(v)
			if perr != nil || n < 1 {
				return opts, fmt.Errorf("--interrupt-after must be a positive integer")
			}
			opts.interruptAfter = n
		case "--resume":
			opts.resume = true
		case "--inject":
			v, err := need()
			if err != nil {
				return opts, err
			}
			if v != "truncate" {
				return opts, fmt.Errorf("--inject only supports truncate")
			}
			opts.inject = v
		case "--mutate":
			v, err := need()
			if err != nil {
				return opts, err
			}
			switch v {
			case "metadata", "replace", "container":
				opts.mutate = v
			default:
				return opts, fmt.Errorf("--mutate must be one of metadata|replace|container")
			}
		case "--threads":
			v, err := need()
			if err != nil {
				return opts, err
			}
			n, perr := parseInt(v)
			if perr != nil || n < 1 {
				return opts, fmt.Errorf("--threads must be a positive integer")
			}
			opts.threads = n
		default:
			return opts, fmt.Errorf("unknown flag %q", a)
		}
	}
	if opts.workspace == "" {
		return opts, fmt.Errorf("--workspace is required")
	}
	if opts.inject != "" && opts.mutate != "" {
		return opts, fmt.Errorf("--inject and --mutate are mutually exclusive")
	}
	return opts, nil
}

func execute(opts options) error {
	if !opts.resume {
		if err := os.RemoveAll(opts.workspace); err != nil {
			return model.WrapError(model.StageWorkspace, 0, 0, "resetting workspace", err)
		}
	}
	if err := os.MkdirAll(opts.workspace, 0o755); err != nil {
		return model.WrapError(model.StageWorkspace, 0, 0, "creating workspace", err)
	}

	spec := fixture.DefaultSpec(opts.frames)
	frames, err := fixture.Generate(opts.workspace, spec)
	if err != nil {
		return err
	}
	if opts.inject == "truncate" {
		// Fault injection: deliberately leave no baseline so the truncated
		// packet surfaces as the first locatable manifest error.
		if frames, err = fixture.Mutate(opts.workspace, spec, frames, fixture.MutateTruncate); err != nil {
			return err
		}
	}
	if opts.mutate != "" {
		// Record the pristine input first, then apply the change so the run
		// reports exactly one invalidation with the correct reason.
		if err = engine.SeedBaseline(opts.workspace); err != nil {
			return err
		}
		mut := fixture.Mutation(opts.mutate)
		if frames, err = fixture.Mutate(opts.workspace, spec, frames, mut); err != nil {
			return err
		}
	}

	cfg := engine.Config{
		Workspace:         opts.workspace,
		Threads:           opts.threads,
		MaxOutputBytes:    opts.maxBytes,
		LoudnessTolerance: opts.lufsTolerance,
		InterruptAfter:    opts.interruptAfter,
		Resume:            opts.resume,
	}

	summary, runErr := engine.Run(cfg)
	if opts.interruptAfter > 0 && opts.resume && errors.Is(runErr, engine.ErrInterrupted) {
		// First leg committed a validated prefix; continue from its checkpoint.
		second := cfg
		second.InterruptAfter = 0
		summary, runErr = engine.Run(second)
	}
	if summary != nil {
		printSummary(summary)
	}
	return runErr
}

func printSummary(s *engine.Summary) {
	fmt.Println("=== continuity run summary ===")
	fmt.Printf("discovered=%d planned=%d processed=%d validated=%d\n",
		s.Discovered, s.Planned, s.Processed, s.Validated)
	fmt.Printf("resume_from=%d\n", s.ResumeFrom)
	fmt.Printf("comparison=%s changed_hashes=%d\n", s.Comparison, s.ChangedHashes)
	fmt.Printf("cache_reason=%s\n", s.CacheReason)
	fmt.Printf("manifest_frames=%d validated_frames=%d\n", s.ManifestFrames, s.Validated)
	fmt.Printf("manifest_sha256=%s\n", orDash(s.ManifestSHA256))
	fmt.Printf("output_sha256=%s\n", orDash(s.OutputSHA256))
	fmt.Printf("cache_invalidations=%d index_invalidations=%d\n",
		s.ContentInvalidations, s.IndexInvalidations)
}

func printError(err error) {
	var se *model.StructuredError
	if errors.As(err, &se) {
		fmt.Fprintf(os.Stderr, "error_stage=%s error_frame=%d error_offset=%d reason=%q\n",
			se.Stage, se.Frame, se.Offset, se.Reason)
		return
	}
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
}

func orDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

func parseInt(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: continuity demo --workspace <dir> [flags]")
	fmt.Fprintln(os.Stderr, "flags: --frames N --interrupt-after N --resume --inject truncate")
	fmt.Fprintln(os.Stderr, "       --mutate metadata|replace|container --threads N")
}
