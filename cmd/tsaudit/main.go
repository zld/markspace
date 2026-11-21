// Command tsaudit audits MPEG-TS files (or standard input): it rebuilds the
// program tables from raw 188-byte packets, tracks PAT/PMT version changes
// and per-PID continuity counters, and can slice one program back out.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/example/tsaudit/internal/analyze"
	"github.com/example/tsaudit/internal/report"
	"github.com/example/tsaudit/internal/slicer"
)

func main() {
	var (
		input     = flag.String("input", "-", "input TS file, or - for standard input")
		format    = flag.String("format", "text", "output format: text|json")
		program   = flag.Uint("program", 0, "program number to slice (with --slice)")
		slicePath = flag.String("slice", "", "write the selected program's packets to this file")
		strict    = flag.Bool("strict-continuity", false, "exit 3 when any continuity gap is found")
	)
	flag.Parse()

	if *format != "text" && *format != "json" {
		fmt.Fprintf(os.Stderr, "tsaudit: unsupported --format %q\n", *format)
		os.Exit(analyze.ExitStructural)
	}

	in, closeIn, err := openInput(*input)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tsaudit:", err)
		os.Exit(analyze.ExitStructural)
	}
	defer closeIn()

	if *slicePath != "" {
		os.Exit(runSlice(in, *slicePath, uint16(*program), *format))
	}
	os.Exit(runAudit(in, *input, *format, *strict))
}

// openInput opens a file or returns stdin when name is empty or "-".
func openInput(name string) (io.Reader, func(), error) {
	if name == "" || name == "-" {
		return os.Stdin, func() {}, nil
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { f.Close() }, nil
}

// runAudit performs the full audit and renders the summary, propagating the
// analyzer's exit code. All diagnostics go to standard error.
func runAudit(in io.Reader, source, format string, strict bool) int {
	az := analyze.New(source, in)
	sum, err := az.Run(strict)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tsaudit:", err)
		return analyze.ExitStructural
	}
	if format == "json" {
		if err := report.JSON(os.Stdout, sum); err != nil {
			fmt.Fprintln(os.Stderr, "tsaudit:", err)
			return analyze.ExitStructural
		}
	} else {
		if err := report.Text(os.Stdout, sum); err != nil {
			fmt.Fprintln(os.Stderr, "tsaudit:", err)
			return analyze.ExitStructural
		}
	}
	for _, e := range sum.StructuralErrors {
		fmt.Fprintln(os.Stderr, "tsaudit:", e)
	}
	return sum.ExitCode
}

// runSlice extracts one program and reports slice statistics.
func runSlice(in io.Reader, path string, program uint16, format string) int {
	if program == 0 {
		fmt.Fprintln(os.Stderr, "tsaudit: --slice requires a non-zero --program")
		return analyze.ExitStructural
	}
	out, err := os.Create(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tsaudit:", err)
		return analyze.ExitStructural
	}
	defer out.Close()

	sl := slicer.New(program)
	res, err := sl.Run(in, out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tsaudit:", err)
		return analyze.ExitStructural
	}
	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			fmt.Fprintln(os.Stderr, "tsaudit:", err)
			return analyze.ExitStructural
		}
	} else {
		fmt.Printf("program=%d written_packets=%d mapping_intervals=%d corrupt_sections=%d\n",
			res.ProgramNumber, res.WrittenPackets, len(res.MappingIntervals),
			len(res.CorruptSections))
		for _, iv := range res.MappingIntervals {
			fmt.Printf("interval elementary_pid=%d stream_type=0x%02x version=%d packets=%d..%d\n",
				iv.ElementaryPID, iv.StreamType, iv.Version, iv.FirstPacket, iv.LastPacket)
		}
	}
	for _, c := range res.CorruptSections {
		fmt.Fprintf(os.Stderr, "tsaudit: corrupt section pid=%d version=%d reason=%s\n",
			c.PID, c.Version, c.Reason)
	}
	if len(res.CorruptSections) > 0 {
		return analyze.ExitStructural
	}
	return analyze.ExitOK
}
