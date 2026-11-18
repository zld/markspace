// Command dashforge generates and validates reproducible MPEG-DASH MPDs from
// local encoding metadata. No media encoding and no network access.
// Exit codes: 0 ok, 2 bad arguments, 3 invalid input/MPD, 4 I/O failure.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"dashforge/internal/fixtures"
	"dashforge/internal/model"
	"dashforge/internal/mpd"
	"dashforge/internal/validate"
)

const (
	exitOK        = 0
	exitArgs      = 2
	exitInvalid   = 3
	exitIOFailure = 4
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitArgs
	}
	switch args[0] {
	case "generate":
		return runGenerate(args[1:], stdout, stderr)
	case "validate":
		return runValidate(args[1:], stdout, stderr)
	case "fixtures":
		return runFixtures(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "error: unknown subcommand %q\n", args[0])
		usage(stderr)
		return exitArgs
	}
}

func parseFlags(fs *flag.FlagSet, cfg *config) {
	fs.StringVar(&cfg.input, "input", "", "path to encoding metadata JSON (required)")
	fs.IntVar(&cfg.timescale, "timescale", 90000, "common MPD timescale in ticks per second")
	fs.Float64Var(&cfg.tolerance, "tolerance-ms", 20, "maximum alignment/duration error in milliseconds")
	fs.BoolVar(&cfg.asJSON, "json", false, "emit the summary as JSON instead of text")
	fs.BoolVar(&cfg.checkFiles, "check-files", false, "stat every referenced segment on disk")
	fs.IntVar(&cfg.maxReps, "max-representations", model.DefaultMaxRepresentations, "representation count safety limit")
	fs.IntVar(&cfg.maxSegs, "max-segments", model.DefaultMaxSegments, "segment count safety limit")
}

func (c config) checkCommon(stderr io.Writer) int {
	switch {
	case c.input == "":
		fmt.Fprintln(stderr, "error: --input is required")
	case c.timescale <= 0:
		fmt.Fprintf(stderr, "error: --timescale must be positive, got %d\n", c.timescale)
	case c.tolerance < 0:
		fmt.Fprintf(stderr, "error: --tolerance-ms must not be negative, got %g\n", c.tolerance)
	case c.maxReps <= 0 || c.maxSegs <= 0:
		fmt.Fprintln(stderr, "error: --max-representations and --max-segments must be positive")
	default:
		return exitOK
	}
	return exitArgs
}

func runGenerate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := config{}
	parseFlags(fs, &cfg)
	fs.StringVar(&cfg.output, "output", "", "MPD output path (default: stdout)")
	if err := fs.Parse(args); err != nil || cfg.checkCommon(stderr) != exitOK {
		return exitArgs
	}
	meta, code := loadMetadata(cfg, stderr)
	if code != exitOK {
		return code
	}
	man, code := normalize(meta, cfg, stderr)
	if code != exitOK {
		return code
	}
	expanded, issues, code := expandAll(man)
	if code != exitOK {
		return code
	}
	findings := append(validate.Model(man, expanded, validate.Options{
		ToleranceMs: cfg.tolerance, CheckFiles: cfg.checkFiles,
	}), issuesToFindings(issues)...)

	sum := buildSummary(man, expanded, cfg.tolerance, findings)
	if len(findings) > 0 { // diagnostics but never an MPD or a success status
		sum.Status = "invalid"
		writeSummary(sum, cfg.asJSON, stdout)
		fmt.Fprintf(stderr, "error: %d validation finding(s); no MPD written\n", len(findings))
		return exitInvalid
	}
	sum.Status = "ok"
	doc := mpd.Build(man)
	if cfg.output == "" {
		if err := mpd.WriteTo(stdout, doc); err != nil {
			fmt.Fprintf(stderr, "error: write MPD: %v\n", err)
			return exitIOFailure
		}
	} else if err := mpd.WriteFile(cfg.output, doc); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitIOFailure
	}
	writeSummary(sum, cfg.asJSON, stdout)
	return exitOK
}

func runValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := config{}
	parseFlags(fs, &cfg)
	fs.StringVar(&cfg.mpd, "mpd", "", "MPD file to cross-check (required)")
	if err := fs.Parse(args); err != nil || cfg.checkCommon(stderr) != exitOK {
		return exitArgs
	}
	if cfg.mpd == "" {
		fmt.Fprintln(stderr, "error: --mpd is required for the validate subcommand")
		return exitArgs
	}
	meta, code := loadMetadata(cfg, stderr)
	if code != exitOK {
		return code
	}
	man, code := normalize(meta, cfg, stderr)
	if code != exitOK {
		return code
	}
	expanded, issues, code := expandAll(man)
	if code != exitOK {
		return code
	}
	findings := validate.Model(man, expanded, validate.Options{
		ToleranceMs: cfg.tolerance, CheckFiles: cfg.checkFiles,
	})
	findings = append(findings, validate.MPD(man, cfg.mpd)...)
	findings = append(findings, issuesToFindings(issues)...)

	sum := buildSummary(man, expanded, cfg.tolerance, findings)
	if len(findings) > 0 {
		sum.Status = "invalid"
		writeSummary(sum, cfg.asJSON, stdout)
		fmt.Fprintf(stderr, "error: %d validation finding(s)\n", len(findings))
		return exitInvalid
	}
	sum.Status = "ok"
	writeSummary(sum, cfg.asJSON, stdout)
	return exitOK
}

func runFixtures(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fixtures", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("out", "testdata/generated", "output directory for fixtures")
	files := fs.Bool("with-files", true, "also create placeholder segment files")
	if err := fs.Parse(args); err != nil {
		return exitArgs
	}
	if err := fixtures.WriteAll(*dir, *files); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitIOFailure
	}
	fmt.Fprintf(stdout, "fixtures written to %s: valid + %d negative sample(s)\n",
		*dir, len(fixtures.Fixtures)-1)
	return exitOK
}

func usage(w io.Writer) {
	fmt.Fprint(w, `dashforge - generate and validate MPEG-DASH MPDs from local metadata

usage:
  dashforge generate --input FILE [--output FILE] [--timescale N] [--tolerance-ms MS] [--json] [--check-files]
  dashforge validate --input FILE --mpd FILE [--timescale N] [--tolerance-ms MS] [--json] [--check-files]
  dashforge fixtures [--out DIR] [--with-files]

exit codes: 0 ok, 2 argument error, 3 invalid input/MPD, 4 read/write failure
`)
}
