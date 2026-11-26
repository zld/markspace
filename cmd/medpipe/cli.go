package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"medpipe/internal/apperr"
	"medpipe/internal/fixture"
	"medpipe/internal/job"
	"medpipe/internal/report"
)

// Main is the testable terminal adapter entry point.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return apperr.ExitGeneric
	}
	switch args[0] {
	case "fixture":
		return runFixture(args[1:], stdout, stderr)
	case "run":
		return runJob(args[1:], stdout, stderr, false)
	case "resume":
		return runJob(args[1:], stdout, stderr, true)
	case "-h", "--help", "help":
		printUsage(stdout)
		return apperr.ExitOK
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", args[0])
		printUsage(stderr)
		return apperr.ExitGeneric
	}
}

func runFixture(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", ".tmp/fixtures", "fixture output directory")
	seed := fs.Int64("seed", 7, "deterministic generator seed")
	if err := fs.Parse(args); err != nil {
		return apperr.ExitGeneric
	}
	res, err := fixture.Generate(fixture.Options{Dir: *dir, Seed: *seed})
	if err != nil {
		printError(stderr, err)
		return apperr.ExitCode(err)
	}
	fmt.Fprintf(stdout, "fixtures generated dir=%s seed=%d manifests=%d media=%d\n", res.Dir, res.Seed, len(res.Manifests), len(res.Media))
	for _, p := range res.Manifests {
		fmt.Fprintf(stdout, "manifest=%s\n", p)
	}
	return apperr.ExitOK
}

func runJob(args []string, stdout, stderr io.Writer, resume bool) int {
	name := "run"
	if resume {
		name = "resume"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	manifestPath := fs.String("manifest", "", "manifest JSON path")
	root := fs.String("root", ".tmp/run", "job state root")
	jobID := fs.String("job-id", "default", "durable job identifier")
	budget := fs.Int("budget-units", 80, "maximum allowed budget units")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON on stdout")
	fault := fs.String("fault", "", "test fault injection point")
	if err := fs.Parse(args); err != nil {
		return apperr.ExitGeneric
	}

	opts := job.Options{Root: *root, JobID: *jobID, BudgetUnits: *budget, Fault: *fault}
	var summary *report.Summary
	var err error
	if resume {
		summary, err = job.Resume(opts)
	} else {
		if strings.TrimSpace(*manifestPath) == "" {
			fmt.Fprintln(stderr, "--manifest is required for run")
			return apperr.ExitGeneric
		}
		summary, err = job.Run(opts, *manifestPath)
	}

	if summary != nil {
		writeReport(summary, *asJSON, stdout, stderr)
	}
	if err != nil {
		code := apperr.ExitCode(err)
		switch {
		case *asJSON && summary == nil:
			writeErrorJSON(stdout, err, code)
			printError(stderr, err)
		case *asJSON && summary != nil:
			// The summary JSON already carries error_code/exit_code; keep
			// stdout a single JSON document and log the error to stderr.
			printError(stderr, err)
		default:
			// Batch callers may inspect stdout alone; terminal users see the
			// same structured conclusion once per stream.
			printError(stdout, err)
			printError(stderr, err)
		}
		return code
	}
	return apperr.ExitOK
}

func writeErrorJSON(w io.Writer, err error, exitCode int) {
	body := map[string]any{"ok": false, "exit_code": exitCode}
	if pe, ok := apperr.As(err); ok {
		body["error"] = pe
	} else {
		body["error"] = map[string]string{"code": "E_INTERNAL", "message": err.Error()}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(body)
}

func writeReport(s *report.Summary, asJSON bool, stdout, stderr io.Writer) {
	human := renderHuman(s)
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		_ = enc.Encode(struct {
			*report.Summary
			Emitted      int    `json:"emitted"`
			DeliveryLine string `json:"delivery_line"`
		}{
			Summary:      s,
			Emitted:      s.Emitted,
			DeliveryLine: fmt.Sprintf("emitted=%d", s.Emitted),
		})
		fmt.Fprint(stderr, human)
		return
	}
	fmt.Fprint(stdout, human)
}

func renderHuman(s *report.Summary) string {
	var b strings.Builder
	b.WriteString(s.SummaryLine)
	b.WriteByte('\n')
	fmt.Fprintf(&b, "emitted=%d\n", s.Emitted)
	for _, line := range s.TerminalLines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func printError(w io.Writer, err error) {
	var pe *apperr.E
	if errors.As(err, &pe) {
		fmt.Fprintf(w, "ERROR code=%s message=%q", pe.Code, pe.Message)
		if pe.Stage != "" {
			fmt.Fprintf(w, " stage=%s", pe.Stage)
		}
		if pe.Track != "" {
			fmt.Fprintf(w, " track=%s", pe.Track)
		}
		if pe.Field != "" {
			fmt.Fprintf(w, " field=%s", pe.Field)
		}
		if pe.SourceFile != "" {
			fmt.Fprintf(w, " source_file=%s", pe.SourceFile)
		}
		if pe.Sample > 0 {
			fmt.Fprintf(w, " sample=%d", pe.Sample)
		}
		if pe.ByteOffset > 0 {
			fmt.Fprintf(w, " byte_offset=%d", pe.ByteOffset)
		}
		fmt.Fprintf(w, " recoverable=%t\n", pe.Recoverable)
		return
	}
	fmt.Fprintf(w, "ERROR code=E_INTERNAL message=%q\n", err.Error())
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `usage:
  medpipe fixture --dir DIR --seed N
  medpipe run --manifest FILE --root DIR --job-id ID [--budget-units N] [--json] [--fault after-artifact]
  medpipe resume --root DIR --job-id ID [--budget-units N] [--json]`)
}
