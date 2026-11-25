// Command regress runs the offline RTP trace regression: it generates sample
// fixtures, executes the staged pipeline with resumable checkpoints, and
// independently verifies the produced artifact.
//
// Exit codes:
//
//	0  verification passed
//	2  input or planning constraint failed
//	3  resume or artifact verification failed
package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"

	"rtpregress/internal/media"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// exitCode maps a structured error to the documented process status.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	switch media.KindOf(err) {
	case media.KindInput, media.KindUnsupported, media.KindBudget, media.KindConflict,
		media.KindTimebase:
		// A broken input or an infeasible plan is the caller's to fix.
		return 2
	case media.KindResume, media.KindVerify, media.KindInterrupt:
		// Recovery and artifact checks are integrity failures.
		return 3
	default:
		return 3
	}
}

type options struct {
	generate       bool
	dir            string
	out            string
	input          string
	budgetMS       float64
	maxBytes       int
	qualityMin     float64
	resume         bool
	interruptAfter string
	idemKey        string
	window         int
	verbose        bool
}

func parseFlags(args []string, stderr io.Writer) (*options, error) {
	fs := flag.NewFlagSet("regress", flag.ContinueOnError)
	fs.SetOutput(stderr)
	o := &options{}
	fs.BoolVar(&o.generate, "generate", false, "generate sample fixtures and exit")
	fs.StringVar(&o.out, "out", "", "fixture output directory for --generate")
	fs.StringVar(&o.input, "input", "", "input directory containing sample.mp4 and video.trace")
	fs.Float64Var(&o.budgetMS, "budget-ms", 500, "maximum processing budget in milliseconds")
	fs.IntVar(&o.maxBytes, "max-bytes", 4096, "maximum delivered artifact size in bytes")
	fs.Float64Var(&o.qualityMin, "quality-min", 0.95, "minimum acceptable quality in [0,1]")
	fs.BoolVar(&o.resume, "resume", false, "resume a previously interrupted run")
	fs.StringVar(&o.interruptAfter, "interrupt-after", "",
		"interrupt after the named stage (parse|align|generate|verify)")
	fs.StringVar(&o.idemKey, "idem-key", "", "idempotency key; defaults to a hash of input and params")
	fs.IntVar(&o.window, "window", 4, "RTP reorder window in packets")
	fs.BoolVar(&o.verbose, "verbose", false, "print per-event mapping lines")
	fs.StringVar(&o.dir, "state-dir", "", "run state directory; defaults alongside the input")
	if err := fs.Parse(args); err != nil {
		return nil, media.Newf(media.KindInput, "%v", err)
	}
	if o.generate {
		if o.out == "" {
			return nil, media.New(media.KindInput, "--generate requires --out")
		}
		return o, nil
	}
	if o.input == "" {
		return nil, media.New(media.KindInput, "--input is required unless --generate is given")
	}
	if o.window < 0 || o.window > 1<<16-1 {
		return nil, media.Newf(media.KindInput,
			"--window must be in [0,%d], got %d", 1<<16-1, o.window)
	}
	if o.interruptAfter != "" && !knownStage(o.interruptAfter) {
		return nil, media.Newf(media.KindInput,
			"--interrupt-after %q is not a pipeline stage", o.interruptAfter)
	}
	return o, nil
}

func knownStage(name string) bool {
	for _, s := range []string{"parse", "align", "generate", "verify"} {
		if s == name {
			return true
		}
	}
	return false
}

func validateDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return media.Newf(media.KindInput, "input directory %s: %v", path, err)
	}
	if !info.IsDir() {
		return media.Newf(media.KindInput, "input path %s is not a directory", path)
	}
	return nil
}

func artifactPath(dir string) string { return filepath.Join(dir, "artifact.bin") }

// filesIn returns the regular files of a directory, sorted by name.
func filesIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out = append(out, e.Name())
	}
	return out, nil
}
