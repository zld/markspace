// Command dashgen generates DASH MPD manifests from local JSON segment
// indexes and verifies cross-rendition time alignment. It never performs
// transcoding or network access.
//
// Exit codes: 0 ok, 2 parameter/input error, 3 timeline validation failure,
// 4 file read/write failure.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"dashgen/internal/align"
	"dashgen/internal/demo"
	"dashgen/internal/mpd"
	"dashgen/internal/packager"
	"dashgen/internal/plan"
	"dashgen/internal/timebase"
	"dashgen/internal/validate"
)

const (
	exitOK        = 0
	exitInput     = 2
	exitTimeline  = 3
	exitFileError = 4
)

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func fail(code int, format string, args ...any) *exitError {
	return &exitError{code: code, err: fmt.Errorf(format, args...)}
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return exitInput
	}
	var err error
	switch args[0] {
	case "demo":
		err = runDemo(args[1:])
	case "build":
		err = runBuild(args[1:])
	case "verify":
		err = runVerify(args[1:])
	case "-h", "--help", "help":
		usage(os.Stdout)
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", args[0])
		usage(os.Stderr)
		return exitInput
	}
	if err == nil {
		return exitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		fmt.Fprintf(os.Stderr, "error: %s\n", ee.Error())
		return ee.code
	}
	fmt.Fprintf(os.Stderr, "error: %s\n", err.Error())
	return exitInput
}

func usage(w *os.File) {
	fmt.Fprint(w, `dashgen - offline DASH manifest generator and alignment checker

usage:
  dashgen demo   --out DIR
  dashgen build  --input plan.json --out stream.mpd --template TEMPLATE
                 [--init-template TEMPLATE]
                 [--start-tolerance DURATION] [--duration-tolerance DURATION]
  dashgen verify --mpd stream.mpd --plan plan.json
                 [--start-tolerance DURATION] [--duration-tolerance DURATION]

durations are integers with a unit suffix: us, ms or s (e.g. 20ms, 1.5s).
exit codes: 0 ok, 2 parameter/input error, 3 timeline failure, 4 file error.
`)
}

func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	outDir := fs.String("out", "", "output directory for plan.json")
	if err := fs.Parse(args); err != nil {
		return fail(exitInput, "demo flags: %w", err)
	}
	if *outDir == "" {
		return fail(exitInput, "demo: --out is required")
	}
	if fs.NArg() != 0 {
		return fail(exitInput, "demo: unexpected positional arguments %v", fs.Args())
	}
	path, err := demo.WritePlan(*outDir)
	if err != nil {
		return fail(exitFileError, "%w", err)
	}
	p := demo.Build()
	fmt.Printf("wrote=%s\n", path)
	fmt.Printf("representation_count=%d\n", len(p.Representations))
	fmt.Printf("segments_per_representation=%d\n", len(p.Representations[0].Segments))
	return nil
}

type toleranceFlags struct {
	start    string
	duration string
}

func (f *toleranceFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&f.start, "start-tolerance", "", "max cross-rep start offset, e.g. 20ms")
	fs.StringVar(&f.duration, "duration-tolerance", "", "max cross-rep duration delta, e.g. 2ms")
}

// parse returns zero-valued rationals for empty flags, meaning "no limit":
// measured maxima are still reported but nothing is rejected.
func (f *toleranceFlags) parse() (validate.Tolerance, error) {
	var tol validate.Tolerance
	if f.start != "" {
		r, err := timebase.ParseDuration(f.start)
		if err != nil {
			return tol, fail(exitInput, "bad --start-tolerance: %w", err)
		}
		tol.StartMs = r
	}
	if f.duration != "" {
		r, err := timebase.ParseDuration(f.duration)
		if err != nil {
			return tol, fail(exitInput, "bad --duration-tolerance: %w", err)
		}
		tol.DurationMs = r
	}
	return tol, nil
}

func loadPlan(path string) (*plan.Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fail(exitFileError, "read plan %s: %w", path, err)
	}
	p, err := plan.Parse(data)
	if err != nil {
		return nil, fail(exitInput, "parse plan %s: %w", path, err)
	}
	return p, nil
}

func runBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	input := fs.String("input", "", "input plan JSON path")
	out := fs.String("out", "", "output MPD path")
	template := fs.String("template", "", "media template, e.g. seg-$RepresentationID$-$Number%05d$.m4s")
	initTemplate := fs.String("init-template", "", "optional initialization segment template")
	var tols toleranceFlags
	tols.bind(fs)
	if err := fs.Parse(args); err != nil {
		return fail(exitInput, "build flags: %w", err)
	}
	if *input == "" || *out == "" || *template == "" {
		return fail(exitInput, "build: --input, --out and --template are required")
	}
	if fs.NArg() != 0 {
		return fail(exitInput, "build: unexpected positional arguments %v", fs.Args())
	}
	tol, err := tols.parse()
	if err != nil {
		return err
	}
	p, err := loadPlan(*input)
	if err != nil {
		return err
	}
	report, alignErr := align.Build(p)
	printTimelineErrors(alignErr)

	pkg, err := packager.Build(p, report, *template, *initTemplate)
	if err != nil {
		return fail(exitInput, "%w", err)
	}
	if err := mpd.WriteFile(*out, pkg.MPD); err != nil {
		return fail(exitFileError, "%w", err)
	}
	fmt.Printf("wrote=%s\n", *out)
	printSummary(pkg.Segments, report, pkg)

	res, terr := validate.CheckTolerances(report, tol)
	if terr != nil {
		return fail(exitInput, "%w", terr)
	}
	printViolations(res)
	if alignErr != nil || res.Failed() {
		return fail(exitTimeline, "timeline validation failed; manifest written for inspection")
	}
	return nil
}

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	mpdPath := fs.String("mpd", "", "generated MPD path")
	planPath := fs.String("plan", "", "source plan JSON path")
	var tols toleranceFlags
	tols.bind(fs)
	if err := fs.Parse(args); err != nil {
		return fail(exitInput, "verify flags: %w", err)
	}
	if *mpdPath == "" || *planPath == "" {
		return fail(exitInput, "verify: --mpd and --plan are required")
	}
	if fs.NArg() != 0 {
		return fail(exitInput, "verify: unexpected positional arguments %v", fs.Args())
	}
	tol, err := tols.parse()
	if err != nil {
		return err
	}
	p, err := loadPlan(*planPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(*mpdPath)
	if err != nil {
		return fail(exitFileError, "read mpd %s: %w", *mpdPath, err)
	}
	doc, err := mpd.Parse(data)
	if err != nil {
		return fail(exitInput, "parse mpd %s: %w", *mpdPath, err)
	}
	v, err := validate.Reconcile(doc, p, tol)
	if err != nil {
		return fail(exitInput, "%w", err)
	}
	for _, f := range v.ReconcileFailures {
		fmt.Printf("reconcile_failure=%s\n", f)
	}
	printSummaryFromVerification(v)
	if v.Tolerance != nil {
		printViolations(v.Tolerance)
	}
	if v.Failed() {
		return fail(exitTimeline, "verification failed")
	}
	fmt.Println("status=ok")
	return nil
}
