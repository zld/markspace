package demo

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"mediacore/internal/index"
	"mediacore/internal/pipeline"
)

// Exit codes required by the acceptance specification.
const (
	ExitOK           = 0
	ExitAssertion    = 1
	ExitUsageOrInput = 2
	ExitChildCrash   = 42
)

type cliOptions struct {
	fixture    string
	rangeText  string
	crashAfter string
	rerun      bool
	all        bool
	workdir    string
	crashChild bool
}

// Main runs the terminal interface and returns a process exit code.
func Main(argv []string) int {
	if len(argv) < 2 {
		usage(os.Stderr)
		return ExitUsageOrInput
	}
	if argv[1] != "demo" {
		fmt.Fprintf(os.Stderr, "unknown command %q; expected demo\n", argv[1])
		usage(os.Stderr)
		return ExitUsageOrInput
	}
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var o cliOptions
	fs.StringVar(&o.fixture, "fixture", "generated", "fixture family: generated or corrupt")
	fs.StringVar(&o.rangeText, "range", "00:10:00,00:10:12", "event-time range start,end")
	fs.StringVar(&o.crashAfter, "crash-after", "", "simulate exit after stage (parse|transform|validate|publish)")
	fs.BoolVar(&o.rerun, "rerun", false, "rerun after simulated crash or completed run")
	fs.BoolVar(&o.all, "all", false, "run all corrupt fixture cases")
	fs.StringVar(&o.workdir, "workdir", ".mediacore-demo", "local working directory")
	fs.BoolVar(&o.crashChild, "crash-child", false, "internal: execute the crashing child attempt")
	if err := fs.Parse(argv[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitOK
		}
		return ExitUsageOrInput
	}
	r, err := ParseRange(o.rangeText)
	if err != nil {
		fmt.Fprintln(os.Stderr, "argument error:", err)
		return ExitUsageOrInput
	}
	switch o.fixture {
	case "generated":
		return runGeneratedCLI(o, r, argv)
	case "corrupt":
		if !o.all {
			fmt.Fprintln(os.Stderr, "corrupt fixture requires --all")
			return ExitUsageOrInput
		}
		return runCorruptCLI(o, r)
	default:
		fmt.Fprintf(os.Stderr, "unknown --fixture %q\n", o.fixture)
		return ExitUsageOrInput
	}
}

func runGeneratedCLI(o cliOptions, r index.Range, argv []string) int {
	workdir, _ := filepath.Abs(filepath.Join(o.workdir, "generated"))
	if o.crashChild {
		return runCrashChild(o, workdir, r)
	}
	if o.crashAfter != "" && o.rerun {
		return runCrashParent(o, r, argv, workdir)
	}
	manifest, err := PrepareGenerated(workdir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "input error:", err)
		return ExitUsageOrInput
	}
	rep, err := RunGenerated(workdir, manifest, r, o.crashAfter)
	if err != nil && !errors.Is(err, pipeline.ErrSimulatedCrash) {
		printReport(rep)
		fmt.Println("acceptance=fail")
		fmt.Fprintln(os.Stderr, "pipeline error:", err)
		return ExitAssertion
	}
	if ok, why := assertGenerated(rep, o.rangeText, false); !ok {
		printReport(rep)
		fmt.Println("acceptance=fail reason=" + why)
		return ExitAssertion
	}
	printReport(rep)
	fmt.Println("acceptance=pass")
	return ExitOK
}

func runCrashChild(o cliOptions, workdir string, r index.Range) int {
	manifest, err := PrepareGenerated(workdir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "input error:", err)
		return ExitUsageOrInput
	}
	rep, err := RunGenerated(workdir, manifest, r, o.crashAfter)
	_ = WritePreCrashReport(workdir, rep)
	if errors.Is(err, pipeline.ErrSimulatedCrash) {
		fmt.Fprintf(os.Stderr, "simulated_exit_after=%s\n", o.crashAfter)
		return ExitChildCrash
	}
	if err != nil {
		printReport(rep)
		fmt.Fprintln(os.Stderr, "child pipeline error:", err)
		return ExitAssertion
	}
	return ExitOK
}

func runCrashParent(o cliOptions, r index.Range, argv []string, workdir string) int {
	if err := os.RemoveAll(workdir); err != nil {
		fmt.Fprintln(os.Stderr, "reset crash workdir:", err)
		return ExitAssertion
	}
	if _, err := PrepareGenerated(workdir); err != nil {
		fmt.Fprintln(os.Stderr, "input error:", err)
		return ExitUsageOrInput
	}
	exe, err := os.Executable()
	if err != nil {
		exe = argv[0]
	}
	childArgs := append(append([]string{}, argv[1:]...), "--crash-child")
	cmd := exec.Command(exe, childArgs...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != ExitChildCrash {
			fmt.Println("acceptance=fail reason=child-did-not-simulate-exit")
			fmt.Fprintln(os.Stderr, "child error:", err)
			return ExitAssertion
		}
	}
	pre, err := ReadPreCrashReport(workdir)
	if err != nil {
		fmt.Println("acceptance=fail reason=missing-pre-crash-report")
		return ExitAssertion
	}
	manifest := filepath.Join(workdir, "fixture", "manifest.json")
	rep, err := RunGenerated(workdir, manifest, r, "")
	if err != nil {
		printReport(rep)
		fmt.Println("acceptance=fail reason=rerun-pipeline-error")
		fmt.Fprintln(os.Stderr, "rerun error:", err)
		return ExitAssertion
	}
	if !SameHashes(pre, rep) {
		printReport(rep)
		fmt.Println("acceptance=fail reason=segment-hash-changed-after-rerun")
		return ExitAssertion
	}
	if ok, why := assertGenerated(rep, o.rangeText, true); !ok {
		printReport(rep)
		fmt.Println("acceptance=fail reason=" + why)
		return ExitAssertion
	}
	printReport(rep)
	fmt.Println("hash_stability=pre_crash_equals_rerun")
	fmt.Println("acceptance=pass")
	return ExitOK
}

func runCorruptCLI(o cliOptions, r index.Range) int {
	workdir, _ := filepath.Abs(filepath.Join(o.workdir, "corrupt"))
	rep, err := RunCorrupt(workdir, r)
	printCorruptReport(rep)
	if err != nil {
		fmt.Println("acceptance=fail")
		fmt.Fprintln(os.Stderr, "corrupt acceptance error:", err)
		return ExitAssertion
	}
	fmt.Println("acceptance=pass")
	return ExitOK
}

func usage(w *os.File) {
	fmt.Fprintln(w, "usage: mediacore demo --fixture {generated|corrupt} [--range HH:MM:SS,HH:MM:SS] [--crash-after publish --rerun] [--all]")
}
