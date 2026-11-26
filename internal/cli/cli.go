// Package cli implements the pure-terminal replayctl acceptance interface.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"replayctl/internal/fault"
	"replayctl/internal/fixture"
	"replayctl/internal/pipeline"
	"replayctl/internal/validate"
)

// Run executes one CLI invocation and returns the process exit status.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  replayctl demo --self-fixture [--case NAME|--truncated-box|--time-gap|--timestamp-wrap|--recover]")
	fmt.Fprintln(w, "                 --event-ms N --pre-ms N --post-ms N --out DIR [--crash-after-rename]")
	fmt.Fprintln(w, "  replayctl verify --dir DIR")
}

type demoFlags struct {
	selfFixture      bool
	caseName         string
	fixtureRoot      string
	out              string
	eventMS          int64
	preMS            int64
	postMS           int64
	crashAfterRename bool
}

func normalizeCase(name string) string {
	switch name {
	case "", "ok", "normal", "success":
		return fixture.CaseOK
	case "truncated-box", "truncated", "corrupt", "corrupted":
		return fixture.CaseTruncated
	case "time-gap", "gap", "empty-gap":
		return fixture.CaseTimeGap
	case "timestamp-wrap", "wrap", "rtp-wrap":
		return fixture.CaseWrap
	case "recover", "recovery", "crash-after-rename":
		return fixture.CaseRecover
	default:
		return name
	}
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f demoFlags
	fs.BoolVar(&f.selfFixture, "self-fixture", false, "generate deterministic local sample data")
	fs.StringVar(&f.caseName, "case", fixture.CaseOK, "fixture case: ok, truncated-box, time-gap, timestamp-wrap, recover")
	fs.StringVar(&f.fixtureRoot, "fixture-dir", "", "optional fixture root (defaults to OS temp)")
	fs.StringVar(&f.out, "out", "", "delivery output directory")
	fs.Int64Var(&f.eventMS, "event-ms", 1000, "event position in fixture milliseconds")
	fs.Int64Var(&f.preMS, "pre-ms", 500, "pre-event half-window in milliseconds")
	fs.Int64Var(&f.postMS, "post-ms", 500, "post-event half-window in milliseconds")
	fs.BoolVar(&f.crashAfterRename, "crash-after-rename", false, "inject exit 75 after final artifact rename")
	var truncated, timeGap, wrap, recover bool
	fs.BoolVar(&truncated, "truncated-box", false, "use the 16-byte declared-longer box fixture")
	fs.BoolVar(&timeGap, "time-gap", false, "place the event in an empty clip segment")
	fs.BoolVar(&wrap, "timestamp-wrap", false, "use the 32-bit RTP timestamp wrap fixture")
	fs.BoolVar(&recover, "recover", false, "enable one-shot rename-then-exit recovery injection")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	switch {
	case truncated:
		f.caseName = fixture.CaseTruncated
	case timeGap:
		f.caseName = fixture.CaseTimeGap
	case wrap:
		f.caseName = fixture.CaseWrap
	case recover:
		f.caseName = fixture.CaseRecover
	}
	f.caseName = normalizeCase(f.caseName)
	if recover || f.caseName == fixture.CaseRecover {
		f.crashAfterRename = true
	}
	if !f.selfFixture {
		fmt.Fprintln(stderr, "demo currently requires --self-fixture")
		return 2
	}
	if strings.TrimSpace(f.out) == "" {
		fmt.Fprintln(stderr, "missing required --out")
		return 2
	}
	if f.eventMS < 0 || f.preMS < 0 || f.postMS < 0 {
		fmt.Fprintln(stderr, "event/pre/post milliseconds must be non-negative")
		return 2
	}
	if f.caseName == fixture.CaseRecover {
		f.crashAfterRename = true
	}

	paths, err := fixture.GenerateSelf(fixture.Options{Root: f.fixtureRoot, Case: f.caseName, EventMS: f.eventMS})
	if err != nil {
		return printFault(err, stderr)
	}
	startMS, endMS := f.eventMS-f.preMS, f.eventMS+f.postMS
	res, err := pipeline.Run(pipeline.Input{
		Out:              f.out,
		MediaPath:        paths.MediaPath,
		EventPath:        paths.EventPath,
		EventID:          paths.EventID,
		StartMS:          startMS,
		EndMS:            endMS,
		Timeline:         paths.Timeline,
		CrashAfterRename: f.crashAfterRename,
	})
	if err != nil {
		return printFault(err, stderr)
	}
	printResult(stdout, f.caseName, res)
	return 0
}

func runVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var dir string
	fs.StringVar(&dir, "dir", "", "delivery directory to verify")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(dir) == "" {
		fmt.Fprintln(stderr, "missing required --dir")
		return 2
	}
	manifest, metrics, err := pipeline.Verify(dir)
	if err != nil {
		return printFault(err, stderr)
	}
	fmt.Fprintln(stdout, "status=verified")
	fmt.Fprintf(stdout, "delivery_key=%s\n", manifest.DeliveryKey)
	printMetrics(stdout, metrics)
	fmt.Fprintf(stdout, "sha256=%s\n", metrics.SHA256)
	return 0
}

func printResult(w io.Writer, caseName string, res *pipeline.Result) {
	fmt.Fprintln(w, "status=ok")
	fmt.Fprintf(w, "case=%s\n", caseName)
	fmt.Fprintf(w, "delivery_key=%s\n", res.DeliveryKey)
	printMetrics(w, res.Metrics)
	fmt.Fprintf(w, "sha256=%s\n", res.SHA256)
	fmt.Fprintf(w, "artifact=%s\n", res.Artifact)
	fmt.Fprintf(w, "recovery recovered=%d published=%d duplicate=%d\n", res.Recovered, res.Published, res.Duplicate)
}

func printMetrics(w io.Writer, m *validate.Metrics) {
	fmt.Fprintf(w, "frames=%d\n", m.Frames)
	fmt.Fprintf(w, "pixels=%d\n", m.Pixels)
	fmt.Fprintf(w, "audio_samples=%d\n", m.AudioSamples)
	fmt.Fprintf(w, "peak_dbfs=%.2f\n", m.PeakDBFS)
	fmt.Fprintf(w, "peak=%.2f dBFS\n", m.PeakDBFS)
	fmt.Fprintf(w, "video_align_error_ms=%d\n", m.VideoAlignErrorMS)
	fmt.Fprintf(w, "audio_boundary_error_samples=%d\n", m.AudioBoundaryErrorSamples)
}

func printFault(err error, w io.Writer) int {
	var fe *fault.Error
	if errors.As(err, &fe) {
		fmt.Fprintf(w, "%s offset=%d", fe.Code, fe.Offset)
		if fe.Path != "" {
			fmt.Fprintf(w, " source=%s", fe.Path)
		}
		if fe.Offset >= 0 {
			fmt.Fprintf(w, " raw_offset=%d", fe.Offset)
		}
		if fe.Tick >= 0 {
			fmt.Fprintf(w, " tick=%d", fe.Tick)
		}
		fmt.Fprintf(w, " stage=%s exit=%d", fe.Stage, fe.ExitCode)
		if fe.Err != nil {
			fmt.Fprintf(w, ": %v", fe.Err)
		}
		fmt.Fprintln(w)
		return fe.ExitCode
	}
	fmt.Fprintf(w, "INTERNAL_ERROR: %v\n", err)
	return 1
}
