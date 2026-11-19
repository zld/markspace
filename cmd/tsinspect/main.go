// Command tsinspect demultiplexes a 188-byte MPEG transport stream, rebuilds
// PAT/PMT program tables, reports continuity anomalies, and optionally slices
// one program into a new transport stream.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"tsinspect/internal/demux"
	"tsinspect/internal/report"
)

// Exit codes are part of the tool's contract.
const (
	ExitOK        = 0
	ExitInput     = 2
	ExitSliceFail = 3
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

type options struct {
	json             bool
	program          int
	out              string
	maxBufferPackets int
	input            string
}

func parseFlags(args []string, stderr io.Writer) (*options, error) {
	fs := flag.NewFlagSet("tsinspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := &options{}
	fs.BoolVar(&opts.json, "json", false, "write a stable JSON summary to stdout")
	fs.IntVar(&opts.program, "program", 0, "program number to slice (0 or omitted disables slicing)")
	fs.StringVar(&opts.out, "out", "", "slice output path; written via a temporary file and atomically replaced")
	fs.IntVar(&opts.maxBufferPackets, "max-buffer-packets", 4096,
		"maximum packets cached while waiting for a program table")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: tsinspect [flags] [input.ts]\n\n"+
			"Reads a 188-byte MPEG-TS from the file or stdin, reports program tables\n"+
			"and continuity anomalies, and optionally slices one program.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 1 {
		return nil, fmt.Errorf("at most one input file may be given, got %d", fs.NArg())
	}
	if fs.NArg() == 1 {
		opts.input = fs.Arg(0)
	}
	if opts.program < 0 || opts.program > 0xffff {
		return nil, fmt.Errorf("-program must be between 0 and 65535, got %d", opts.program)
	}
	if opts.maxBufferPackets <= 0 {
		return nil, fmt.Errorf("-max-buffer-packets must be positive, got %d", opts.maxBufferPackets)
	}
	if opts.out != "" && opts.program == 0 {
		return nil, errors.New("-out requires -program")
	}
	return opts, nil
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opts, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitOK
		}
		fmt.Fprintf(stderr, "tsinspect: %v\n", err)
		return ExitInput
	}

	in := stdin
	// "-" is the conventional spelling of standard input.
	if opts.input != "" && opts.input != "-" {
		f, err := os.Open(opts.input)
		if err != nil {
			fmt.Fprintf(stderr, "tsinspect: opening input: %v\n", err)
			return ExitInput
		}
		defer f.Close()
		in = f
	}
	inputName := opts.input
	if inputName == "" || inputName == "-" {
		inputName = "<stdin>"
	}

	var outFile *atomicFile
	var sliceWriter io.Writer
	if opts.out != "" {
		af, err := createAtomic(opts.out)
		if err != nil {
			fmt.Fprintf(stderr, "tsinspect: preparing output: %v\n", err)
			return ExitSliceFail
		}
		outFile = af
		sliceWriter = af
	}

	cfg := demux.Config{
		Program:          opts.program,
		MaxBufferPackets: opts.maxBufferPackets,
		SliceToDisk:      outFile != nil,
	}
	pipeline := demux.NewPipeline(in, cfg, sliceWriter)
	res, runErr := pipeline.Run()
	sliceErr := pipeline.SliceError()
	integrityErr := pipeline.IntegrityError()

	// A write failure must not replace the previous output file. The
	// temporary file is removed, so any older destination stays intact.
	// PSI integrity failures still commit: the slice itself is not corrupt.
	if outFile != nil {
		if err := outFile.Finish(sliceErr == nil && runErr == nil); err != nil {
			fmt.Fprintf(stderr, "tsinspect: finalizing output: %v\n", err)
			sliceErr = err
		}
	}

	exit := ExitOK
	var inputErrs []string
	if sliceErr != nil {
		inputErrs = append(inputErrs, sliceErr.Error())
		exit = ExitSliceFail
	}
	if runErr != nil {
		inputErrs = append(inputErrs, runErr.Error())
		if exit == ExitOK {
			exit = ExitInput
		}
	}
	if integrityErr != nil && exit == ExitOK {
		msg := fmt.Sprintf("%v: %d CRC, %d section, %d table", integrityErr,
			res.CRCErrors, res.SectionErrors, res.TableErrors)
		inputErrs = append(inputErrs, msg)
		fmt.Fprintf(stderr, "tsinspect: %s\n", msg)
		exit = ExitInput
	}

	var sliceStats *demux.SliceStats
	if res != nil && opts.program > 0 {
		st := res.Slice
		sliceStats = &st
	}

	if opts.json && res != nil {
		summary := report.Build(inputName, res, sliceStats, inputErrs)
		if err := report.WriteJSON(stdout, summary); err != nil {
			fmt.Fprintf(stderr, "tsinspect: %v\n", err)
			return ExitInput
		}
	} else if res != nil {
		printHuman(stdout, inputName, res, opts.program)
		if len(inputErrs) > 0 {
			fmt.Fprintf(stdout, "errors: %v\n", inputErrs)
		}
	}

	if runErr != nil {
		fmt.Fprintf(stderr, "tsinspect: %v\n", runErr)
	}
	return exit
}

func printHuman(w io.Writer, name string, res *demux.Result, program int) {
	fmt.Fprintf(w, "input: %s\n", name)
	fmt.Fprintf(w, "packets processed: %d (%d bytes)\n", res.Packets, res.Bytes)
	fmt.Fprintf(w, "pat version changes: %d\n", res.Programs.PATVersionChanges())
	fmt.Fprintf(w, "pmt version changes: %d\n", res.Programs.PMTVersionChanges())
	for _, num := range res.Programs.Programs() {
		pmt := res.Programs.PMT(num)
		fmt.Fprintf(w, "program %d: pmt_pid=%d pcr_pid=%d version=%d streams=%d\n",
			num, pmt.PID, pmt.PCRPID, pmt.Version, len(pmt.Streams))
	}
	tot := res.Metrics.Totals()
	fmt.Fprintf(w, "anomalies: duplicates=%d jumps=%d transport_errors=%d af_only=%d unjudgeable=%d crc_errors=%d section_errors=%d orphan_pmts=%d\n",
		tot.Duplicates, tot.Jumps, tot.TransportErrors, tot.AFOnly, tot.UnknownGaps,
		res.CRCErrors, res.SectionErrors, res.Programs.OrphanPMTs())
	if program > 0 {
		fmt.Fprintf(w, "slice program %d: packets=%d pmt=%d es=%d boundaries=%d buffered_flushed=%d dropped=%d overflows=%d\n",
			program, res.Slice.PacketsWritten, res.Slice.PMTPackets, res.Slice.ESPackets,
			len(res.Slice.Boundaries), res.Slice.BufferedFlushed,
			res.Slice.BufferedDropped, res.Slice.BufferOverflows)
	}
}
