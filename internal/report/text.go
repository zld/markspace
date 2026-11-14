// Package report renders analysis results.
//
// The text renderer is the human-facing default; the JSON renderer emits a
// stable, machine-readable document with a summary object and a block array.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"bbdiff/internal/analyze"
	"bbdiff/internal/elfscan"
)

// JSONVersion identifies the JSON document shape.
const JSONVersion = 1

// JSONDocument is the top-level JSON output.
type JSONDocument struct {
	Version     int                      `json:"version"`
	Summary     analyze.Summary          `json:"summary"`
	Blocks      []analyze.Diff           `json:"blocks"`
	Functions   []analyze.FunctionReport `json:"functions,omitempty"`
	Diagnostics []elfscan.Diagnostic     `json:"diagnostics,omitempty"`
	Old         string                   `json:"old"`
	New         string                   `json:"new"`
	Cancelled   bool                     `json:"cancelled,omitempty"`
	Failures    []string                 `json:"failures,omitempty"`
}

// NewJSONDocument converts an analysis result into the JSON document shape.
func NewJSONDocument(res *analyze.Result) JSONDocument {
	if res == nil {
		return JSONDocument{Version: JSONVersion, Blocks: []analyze.Diff{}}
	}
	doc := JSONDocument{
		Version:     JSONVersion,
		Summary:     res.Summary,
		Blocks:      res.Blocks,
		Functions:   res.Functions,
		Diagnostics: res.Diagnostics,
		Old:         res.OldPath,
		New:         res.NewPath,
		Cancelled:   res.Cancelled,
		Failures:    res.Failures,
	}
	if doc.Blocks == nil {
		doc.Blocks = []analyze.Diff{}
	}
	return doc
}

// WriteJSON writes the JSON document indented with two spaces.
func WriteJSON(w io.Writer, res *analyze.Result) error {
	doc := NewJSONDocument(res)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("report: encode JSON: %w", err)
	}
	return nil
}

// Options controls text rendering.
type Options struct {
	// ShowDiagnostics prints the diagnostic section.
	ShowDiagnostics bool
	// ShowFunctions prints the per-function table.
	ShowFunctions bool
	// MaxBlocks limits the number of block lines; zero means all.
	MaxBlocks int
}

// DefaultOptions returns the terminal defaults.
func DefaultOptions() Options {
	return Options{ShowDiagnostics: true, ShowFunctions: true}
}

// WriteText renders a human-readable report.
func WriteText(w io.Writer, res *analyze.Result, opt Options) error {
	if res == nil {
		_, err := fmt.Fprintln(w, "bbdiff: no result")
		return err
	}
	bw := &errWriter{w: w}

	bw.printf("bbdiff: basic-block diff (x86-64, Capstone 5)\n")
	bw.printf("  old: %s\n", res.OldPath)
	bw.printf("  new: %s\n", res.NewPath)
	if res.Cancelled {
		bw.printf("  !! analysis was cancelled; results are partial\n")
	}
	bw.printf("\nsummary: added=%d removed=%d changed=%d uncertain=%d\n",
		res.Summary.Added, res.Summary.Removed, res.Summary.Changed, res.Summary.Uncertain)
	bw.printf("         matched functions=%d, blocks reported=%d\n",
		countAligned(res.Functions), len(res.Blocks))

	if len(res.Failures) > 0 {
		bw.printf("\nfailures (%d):\n", len(res.Failures))
		for _, f := range res.Failures {
			bw.printf("  ! %s\n", f)
		}
	}

	if len(res.Blocks) > 0 {
		bw.printf("\nchanged blocks:\n")
		bw.printf("  %-10s %-10s %-10s %-9s %-5s %-6s %s\n", "KIND", "ADDRESS", "FUNCTION", "INSN", "CONF", "SHA256", "DETAIL")
		shown := 0
		for _, b := range res.Blocks {
			if opt.MaxBlocks > 0 && shown >= opt.MaxBlocks {
				bw.printf("  ... %d more blocks omitted (use --max-blocks 0 to show all)\n", len(res.Blocks)-shown)
				break
			}
			detail := ""
			if b.OldAddress != "" && b.NewAddress != "" {
				detail = b.OldAddress + " -> " + b.NewAddress
			}
			if b.ChangedInstructions > 0 {
				detail += fmt.Sprintf(" insns±%d", b.ChangedInstructions)
			}
			if b.Confidence > 0 && b.Kind == "uncertain" {
				detail += fmt.Sprintf(" conf=%d%%", b.Confidence)
			}
			hash := b.SHA256
			if len(hash) > 16 {
				hash = hash[:16] + "…"
			}
			bw.printf("  %-10s %-10s %-10s %-9d %-5s %-6s %s\n",
				b.Kind, b.Address, truncate(b.Function, 10), b.InstructionCount,
				confidenceCell(b), hash, strings.TrimSpace(detail))
			if len(b.Reasons) > 0 {
				bw.printf("  %-10s %s\n", "", strings.Join(b.Reasons, "; "))
			}
			shown++
		}
	} else {
		bw.printf("\nno basic-block differences found\n")
	}

	if opt.ShowFunctions && len(res.Functions) > 0 {
		bw.printf("\nfunctions:\n")
		bw.printf("  %-20s %-11s %-11s %-8s %-7s %-9s %s\n", "NAME", "OLD", "NEW", "STATUS", "BLOCKS", "CHANGED", "UNCERTAIN")
		fns := append([]analyze.FunctionReport(nil), res.Functions...)
		sort.SliceStable(fns, func(i, j int) bool { return fns[i].Name < fns[j].Name })
		for _, fn := range fns {
			bw.printf("  %-20s %-11s %-11s %-8s %d/%-5d %-9d %d\n",
				truncate(fn.Name, 20), orDash(fn.OldAddress), orDash(fn.NewAddress),
				fn.Status, fn.OldBlocks, fn.NewBlocks, fn.Changed, fn.Uncertain)
		}
	}

	if opt.ShowDiagnostics && len(res.Diagnostics) > 0 {
		bw.printf("\ndiagnostics (%d):\n", len(res.Diagnostics))
		for _, d := range res.Diagnostics {
			bw.printf("  [%s] %s: %s\n", d.Severity, d.Code, d.Message)
		}
	}
	return bw.err
}

func confidenceCell(b analyze.Diff) string {
	if b.Kind != "uncertain" {
		return "-"
	}
	if b.Confidence > 0 {
		return fmt.Sprintf("%d%%", b.Confidence)
	}
	return "?"
}

func countAligned(fns []analyze.FunctionReport) int {
	n := 0
	for _, f := range fns {
		if f.Status == "aligned" || f.Status == "uncertain" {
			n++
		}
	}
	return n
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, args...)
}
