// subx converts SRT/WebVTT subtitles across formats and frame rates with
// per-cue round-trip verification.
//
// Exit codes: 0 success; 2 parameter or file I/O error; 3 subtitle format
// or encoding error; 4 serialization round-trip mismatch.
package main

import (
	"fmt"
	"os"

	"subx/internal/batch"
	"subx/internal/cli"
	"subx/internal/fixture"
	"subx/internal/report"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	opts, err := cli.Parse(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return batch.CodeIO
	}
	switch opts.Cmd {
	case "fixture":
		paths, err := fixture.Generate(opts.Dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return batch.CodeIO
		}
		for _, p := range paths {
			fmt.Printf("created=%s\n", p)
		}
		return batch.CodeOK
	case "convert":
		rep := batch.ConvertOne(batch.Job{Input: opts.Input, Output: opts.Output}, opts.Batch)
		return finish([]report.FileReport{rep}, opts.Report)
	case "batch":
		jobs, err := batch.Plan(opts.InputDir, opts.OutputDir, opts.Batch.To)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return batch.CodeIO
		}
		return finish(batch.Run(jobs, opts.Batch), opts.Report)
	}
	return batch.CodeIO
}

// finish renders reports, writes the optional JSON report and derives the
// process exit code: the most severe per-file code wins (4 > 3 > 2).
func finish(reps []report.FileReport, reportPath string) int {
	report.RenderTerminal(os.Stdout, reps)
	code := batch.CodeOK
	rank := map[int]int{batch.CodeIO: 1, batch.CodeFormat: 2, batch.CodeRoundTrip: 3}
	for _, r := range reps {
		if rank[r.Code] > rank[code] {
			code = r.Code
		}
	}
	if reportPath != "" {
		data, err := report.JSON(reps)
		if err == nil {
			err = batch.WriteFileAtomic(reportPath, append(data, '\n'))
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: write report: %v\n", err)
			if code == batch.CodeOK {
				code = batch.CodeIO
			}
		}
	}
	return code
}
