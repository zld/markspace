// Command loudcheck measures loudness and true peak of local WAV files.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"loudcheck/internal/batch"
	"loudcheck/internal/cli"
	"loudcheck/internal/fixture"
	"loudcheck/internal/report"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(argv []string, stdout, stderr io.Writer) int {
	opts, err := cli.Parse(argv, stderr)
	if err != nil {
		if err == flag.ErrHelp {
			return cli.ExitOK
		}
		fmt.Fprintf(stderr, "loudcheck: %v\n", err)
		return cli.ExitUsage
	}

	switch opts.Command {
	case "synth":
		return runSynth(opts, stdout, stderr)
	case "analyze":
		return runAnalyze(opts, stdout, stderr)
	case "batch":
		return runBatch(opts, stdout, stderr)
	}
	fmt.Fprintf(stderr, "loudcheck: unknown command %q\n", opts.Command)
	return cli.ExitUsage
}

func runSynth(opts cli.Options, stdout, stderr io.Writer) int {
	if err := writeSynthFile(opts); err != nil {
		fmt.Fprintf(stderr, "loudcheck: %v\n", err)
		return cli.ExitUsage
	}
	// Verify the freshly written file with the same analyzer the batch path
	// uses; this keeps synth honest about what it produced.
	summary := batch.AnalyzeFiles([]string{opts.SynthOut}, analyzeOptions(opts))
	if err := report.WriteBatch(stdout, summary, opts.Format); err != nil {
		fmt.Fprintf(stderr, "loudcheck: %v\n", err)
		return cli.ExitUsage
	}
	return report.ExitCode(summary, false)
}

func writeSynthFile(opts cli.Options) error {
	path := opts.SynthOut
	if path == "" {
		return fmt.Errorf("synth: no output path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	sig := fixture.Signal(opts.Synth)
	if len(sig) == 0 {
		return fmt.Errorf("synth: generated signal is empty")
	}
	data := fixture.Encode(opts.Synth, sig)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func runAnalyze(opts cli.Options, stdout, stderr io.Writer) int {
	summary := batch.AnalyzeFiles(opts.Files, analyzeOptions(opts))
	if err := report.WriteBatch(stdout, summary, opts.Format); err != nil {
		fmt.Fprintf(stderr, "loudcheck: %v\n", err)
		return cli.ExitUsage
	}
	return report.ExitCode(summary, false)
}

func runBatch(opts cli.Options, stdout, stderr io.Writer) int {
	paths := append([]string(nil), opts.Files...)
	if opts.InputDir != "" {
		found, err := batch.ExpandPath(opts.InputDir)
		if err != nil {
			fmt.Fprintf(stderr, "loudcheck: %v\n", err)
			return cli.ExitUsage
		}
		paths = append(paths, found...)
	}
	summary := batch.AnalyzeFiles(paths, analyzeOptions(opts))
	if !opts.Quiet && opts.Format == report.Text {
		fmt.Fprintln(stderr, report.ThresholdSummary(opts.Thresholds))
	}
	if err := report.WriteBatch(stdout, summary, opts.Format); err != nil {
		fmt.Fprintf(stderr, "loudcheck: %v\n", err)
		return cli.ExitUsage
	}
	return report.ExitCode(summary, false)
}

func analyzeOptions(opts cli.Options) batch.Options {
	workers := opts.Workers
	if workers < 1 {
		workers = 1
	}
	return batch.Options{
		LayoutName: opts.LayoutName,
		Mapping:    opts.Mapping,
		Thresholds: opts.Thresholds,
		Workers:    workers,
	}
}
