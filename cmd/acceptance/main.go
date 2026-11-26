package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mediacheck/internal/cache"
	"mediacheck/internal/timebase"
	"mediacheck/internal/wav"
)

const (
	exitOK          = 0
	exitFailure     = 1
	exitUsage       = 2
	exitExternalBad = 3
)

type config struct {
	caseName string
	out      string
	input    string
}

func printJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(os.Stderr, "encode report:", err)
	}
}

func checkExternalInput(path string, stdout io.Writer) int {
	f, err := os.Open(path)
	if err != nil {
		printJSON(stdout, Report{
			Case: "external_input", OK: false, BadOffset: -1,
			ErrorCode: string(timebase.CodeExternalBad), Error: err.Error(),
		})
		return exitExternalBad
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		printJSON(stdout, Report{Case: "external_input", OK: false, BadOffset: -1, Error: err.Error()})
		return exitExternalBad
	}
	if _, err := wav.OpenAt(f, st.Size()); err != nil {
		te := canonicalError(err)
		rep := Report{Case: "external_input", OK: false, BadOffset: -1, Error: err.Error()}
		if te != nil {
			rep.BadOffset = te.Offset
			rep.ErrorCode = string(te.Code)
		} else {
			rep.ErrorCode = string(timebase.CodeExternalBad)
		}
		printJSON(stdout, rep)
		return exitExternalBad
	}
	printJSON(stdout, Report{Case: "external_input", OK: true, BadOffset: -1})
	return exitOK
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("acceptance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := config{}
	fs.StringVar(&cfg.caseName, "case", "all", "case to run: all, normal, damage, recovery, repeat")
	fs.StringVar(&cfg.out, "out", "./media-check-output", "directory for fixtures, fragments, manifests and cache")
	fs.StringVar(&cfg.input, "input", "", "optional external WAV to validate")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected positional arguments: %v\n", fs.Args())
		return exitUsage
	}
	switch cfg.caseName {
	case "all", "normal", "damage", "recovery", "repeat":
	default:
		fmt.Fprintf(stderr, "invalid --case %q\n", cfg.caseName)
		return exitUsage
	}
	if cfg.input != "" {
		return checkExternalInput(cfg.input, stdout)
	}
	if err := os.MkdirAll(cfg.out, 0o755); err != nil {
		fmt.Fprintln(stderr, "create --out:", err)
		return exitFailure
	}

	var reports []Report
	switch cfg.caseName {
	case "normal":
		reports = []Report{caseNormal(cfg.out)}
	case "damage":
		reports = []Report{caseDamage(cfg.out)}
	case "recovery":
		reports = []Report{caseRecovery(cfg.out)}
	case "repeat":
		reports = []Report{caseRepeat(cfg.out)}
	default:
		reports = []Report{
			caseNormal(cfg.out),
			caseDamage(cfg.out),
			caseRecovery(cfg.out),
			caseRepeat(cfg.out),
		}
	}

	allOK := true
	for _, rep := range reports {
		printJSON(stdout, rep)
		if !rep.OK {
			allOK = false
		}
	}
	if summary, err := json.MarshalIndent(reports, "", "  "); err == nil {
		_ = cache.AtomicWriteFile(filepath.Join(cfg.out, "reports.json"), append(summary, '\n'), 0o644)
	}
	if !allOK {
		return exitFailure
	}
	return exitOK
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
