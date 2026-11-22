// Command dashcheck turns multi-bitrate encoding metadata into a
// single-Period MPEG-DASH MPD, refusing to publish when representation
// boundaries cannot be safely aligned.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"dashcheck/internal/align"
	"dashcheck/internal/fixture"
	"dashcheck/internal/input"
	"dashcheck/internal/manifest"
	"dashcheck/internal/model"
	"dashcheck/internal/mpd"
	"dashcheck/internal/report"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "validate":
		return runValidate(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown subcommand %q\n", args[0])
		usage(stderr)
		return 2
	}
}

type validateConfig struct {
	inputPath  string
	mpdPath    string
	reportPath string
	tolerance  string
	loose      bool
}

func runValidate(args []string, stdout, stderr io.Writer) int {
	cfg, err := parseValidateFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	tolerance, err := model.ParseRat(cfg.tolerance)
	if err != nil {
		return fail(err, stderr)
	}
	if c, _ := tolerance.Compare(model.ZeroRat); c < 0 {
		return fail(model.Errorf(model.CategoryInput, model.CodeInvalidValue,
			"tolerance must not be negative, got %s", cfg.tolerance), stderr)
	}
	if err := guardPaths(cfg); err != nil {
		return fail(err, stderr)
	}

	desc, err := input.Load(cfg.inputPath)
	if err != nil {
		return fail(err, stderr)
	}
	res, alignErr := align.Align(desc, tolerance, cfg.loose)
	if res == nil {
		return fail(alignErr, stderr)
	}
	m, err := manifest.Build(desc.ContentID, res)
	if err != nil {
		return fail(err, stderr)
	}

	written := false
	if alignErr == nil && !cfg.loose {
		if err := mpd.Write(m, cfg.mpdPath); err != nil {
			return fail(err, stderr)
		}
		written = true
	}

	reportOut, closeReport, err := openReport(cfg.reportPath)
	if err != nil {
		return fail(err, stderr)
	}
	defer closeReport()
	if err := report.Write(io.MultiWriter(stdout, reportOut), m, report.Options{
		MPDWritten: written, MPDPath: cfg.mpdPath, Loose: cfg.loose,
	}); err != nil {
		return fail(model.Errorf(model.CategoryIO, model.CodeWriteFailed,
			"cannot write report: %v", err), stderr)
	}

	if alignErr != nil {
		if cfg.loose {
			fmt.Fprintf(stderr, "loose diagnostic mode: MPD intentionally not written (%s)\n",
				alignErr.Error())
		} else {
			fmt.Fprintln(stderr, alignErr.Error())
		}
		return model.ExitCode(alignErr)
	}
	if !written {
		fmt.Fprintln(stderr, "no MPD produced and no alignment error recorded")
		return 3
	}
	return 0
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	outDir := fs.String("out", "", "output directory for the demo description and MPD")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *outDir == "" {
		fmt.Fprintln(stderr, "demo requires --out")
		return 2
	}
	inputPath, err := fixture.WriteDemo(*outDir)
	if err != nil {
		return fail(err, stderr)
	}
	mpdPath := filepath.Join(*outDir, "output.mpd")
	fmt.Fprintf(stdout, "demo input written to %s\n", inputPath)
	return runValidate([]string{
		"--input", inputPath,
		"--tolerance", "1/1000",
		"--mpd", mpdPath,
		"--report", filepath.Join(*outDir, "report.txt"),
	}, stdout, stderr)
}

func parseValidateFlags(args []string) (validateConfig, error) {
	cfg := validateConfig{tolerance: "1/1000"}
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.StringVar(&cfg.inputPath, "input", "", "path to representations.json")
	fs.StringVar(&cfg.mpdPath, "mpd", "", "output MPD path (publish mode)")
	fs.StringVar(&cfg.reportPath, "report", "", "optional report file; report always goes to stdout too")
	fs.StringVar(&cfg.tolerance, "tolerance", cfg.tolerance, "boundary skew tolerance in seconds, e.g. 1/1000")
	fs.BoolVar(&cfg.loose, "loose", false, "diagnostic-only mode: report skews but never write an MPD")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if cfg.inputPath == "" {
		return cfg, model.Errorf(model.CategoryInput, model.CodeMissingField,
			"validate requires --input")
	}
	if !cfg.loose && cfg.mpdPath == "" {
		return cfg, model.Errorf(model.CategoryInput, model.CodeMissingField,
			"publish mode requires --mpd (or pass --loose for diagnostics)")
	}
	return cfg, nil
}

func guardPaths(cfg validateConfig) error {
	if cfg.mpdPath != "" {
		if absEq, err := samePath(cfg.inputPath, cfg.mpdPath); err != nil {
			return err
		} else if absEq {
			return model.Errorf(model.CategoryIO, model.CodePathConflict,
				"refusing to write MPD over input file %q", cfg.inputPath)
		}
		if dir, file := filepath.Split(cfg.mpdPath); dir != "" && file == "" {
			return model.Errorf(model.CategoryIO, model.CodePathConflict,
				"--mpd must name a file, got directory path %q", cfg.mpdPath)
		}
	}
	if cfg.reportPath != "" {
		if same, err := samePath(cfg.inputPath, cfg.reportPath); err != nil {
			return err
		} else if same {
			return model.Errorf(model.CategoryIO, model.CodePathConflict,
				"refusing to write report over input file %q", cfg.inputPath)
		}
	}
	return nil
}

func samePath(a, b string) (bool, error) {
	absA, err := filepath.Abs(a)
	if err != nil {
		return false, model.Errorf(model.CategoryIO, model.CodePathConflict,
			"cannot resolve path %q: %v", a, err)
	}
	absB, err := filepath.Abs(b)
	if err != nil {
		return false, model.Errorf(model.CategoryIO, model.CodePathConflict,
			"cannot resolve path %q: %v", b, err)
	}
	return absA == absB, nil
}

func openReport(path string) (io.Writer, func(), error) {
	if path == "" {
		return io.Discard, func() {}, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, nil, model.Errorf(model.CategoryIO, model.CodeWriteFailed,
			"cannot open report %q: %v", path, err)
	}
	return f, func() { _ = f.Close() }, nil
}

func fail(err error, stderr io.Writer) int {
	fmt.Fprintln(stderr, err.Error())
	return model.ExitCode(err)
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  dashcheck demo --out DIR")
	fmt.Fprintln(w, "  dashcheck validate --input FILE --tolerance a/b --mpd FILE [--report FILE] [--loose]")
}
