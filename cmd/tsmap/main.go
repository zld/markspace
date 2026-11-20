// tsmap is a terminal tool to parse, analyze and slice 188-byte MPEG-TS
// files: it rebuilds program tables, tracks continuity counters and cuts
// slices with fresh PAT/PMT injected at each head.
//
// Exit codes:
//
//	0  summary generated (classified anomalies may still be present)
//	1  argument or file read/write error
//	2  cannot resynchronize / structural damage
//	3  input ended without a complete valid program table
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"tsmap/internal/demux"
	"tsmap/internal/fixture"
	"tsmap/internal/report"
	"tsmap/internal/slicer"
)

const (
	exitOK = iota
	exitUsageOrIO
	exitResync
	exitNoTables
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitUsageOrIO
	}
	var code int
	switch args[0] {
	case "fixture":
		code = cmdFixture(args[1:])
	case "analyze":
		code = cmdAnalyze(args[1:])
	case "slice":
		code = cmdSlice(args[1:])
	default:
		usage()
		code = exitUsageOrIO
	}
	return code
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  tsmap fixture --out FILE [--profile versions-and-gap]
  tsmap analyze --input FILE [--format text|json]
  tsmap slice   --input FILE --out-dir DIR --max-packets N`)
}

func cmdFixture(args []string) int {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	out := fs.String("out", "", "output file path")
	profile := fs.String("profile", fixture.ProfileVersionsAndGap, "sample profile")
	if fs.Parse(args) != nil || *out == "" {
		usage()
		return exitUsageOrIO
	}
	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitUsageOrIO
	}
	defer f.Close()
	if err := fixture.Generate(f, *profile); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitUsageOrIO
	}
	fmt.Printf("fixture written: %s (profile %s)\n", *out, *profile)
	return exitOK
}

func cmdAnalyze(args []string) int {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	input := fs.String("input", "", "input TS file")
	format := fs.String("format", "text", "text or json")
	if fs.Parse(args) != nil || *input == "" {
		usage()
		return exitUsageOrIO
	}
	s, err := demux.AnalyzeFile(*input)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitUsageOrIO
	}
	switch *format {
	case "json":
		err = report.WriteJSON(os.Stdout, s)
	case "text":
		err = report.WriteText(os.Stdout, s)
	default:
		fmt.Fprintln(os.Stderr, "unknown --format:", *format)
		return exitUsageOrIO
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitUsageOrIO
	}
	return fatalExitCode(s.Fatal)
}

func cmdSlice(args []string) int {
	fs := flag.NewFlagSet("slice", flag.ContinueOnError)
	input := fs.String("input", "", "input TS file")
	outDir := fs.String("out-dir", "", "output directory")
	maxPackets := fs.Int("max-packets", 0, "max input packets per slice")
	if fs.Parse(args) != nil || *input == "" || *outDir == "" || *maxPackets <= 0 {
		usage()
		return exitUsageOrIO
	}
	res, err := slicer.Slice(*input, slicer.Options{OutDir: *outDir, MaxPackets: *maxPackets})
	fmt.Printf("slices: %d\n", len(res.Files))
	for i, f := range res.Files {
		fmt.Printf("  part-%06d.ts packets=%d injected_pat=%d injected_pmt=%d\n",
			i+1, f.Packets, f.InjectedPAT, f.InjectedPMT)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, demux.ErrResync) {
			return exitResync
		}
		return exitUsageOrIO
	}
	return exitOK
}

func fatalExitCode(fatal string) int {
	switch fatal {
	case demux.FatalNone:
		return exitOK
	case demux.FatalNoTables:
		return exitNoTables
	case demux.FatalResync:
		return exitResync
	default:
		return exitUsageOrIO
	}
}
