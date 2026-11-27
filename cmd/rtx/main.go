// Command rtx is the acceptance adapter for the offline RTP/WAV
// regression pipeline. It generates deterministic samples, runs the
// pipeline and prints the JSON evidence summary. Exit codes: 0 all
// thresholds pass, 1 structural/parse error, 2 infeasible budget.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"rtx/internal/model"
	"rtx/internal/pipeline"
	"rtx/internal/report"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 || args[0] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: rtx demo [--seed N] [--channels N] [--sample-rate N]")
		fmt.Fprintln(os.Stderr, "       [--budget-ms N] [--max-drift-ppm N] [--corrupt a,b]")
		return report.ExitParse
	}
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Int64("seed", 17, "deterministic sample seed")
	channels := fs.Int("channels", 2, "WAV channel count")
	sampleRate := fs.Int("sample-rate", 48000, "WAV sample rate and RTP clock rate")
	budgetMS := fs.Int64("budget-ms", 120, "processing budget in milliseconds")
	maxDriftPPM := fs.Float64("max-drift-ppm", 300, "drift threshold in ppm")
	corrupt := fs.String("corrupt", "", "comma-separated corruptions: tail,truncated")
	minQuality := fs.Float64("min-quality", 0.3, "minimum acceptable quality score")
	maxBitrate := fs.Int64("max-bitrate", 1_000_000, "bitrate ceiling in bit/s")
	targetBPS := fs.Int64("target-bitrate", 128_000, "target bitrate in bit/s")
	if err := fs.Parse(args[1:]); err != nil {
		return report.ExitParse
	}
	if *channels < 1 || *sampleRate < 8000 {
		fmt.Fprintln(os.Stderr, "invalid channels or sample rate")
		return report.ExitParse
	}
	c := parseCorrupt(strings.Split(*corrupt, ","))
	wavBuf := generateWAV(*seed, *channels, *sampleRate, c)
	rtpBuf := generateRTP(*seed, *sampleRate)
	mp4Buf := generateMP4(*sampleRate)
	opts := pipeline.Options{
		ParseOptions: pipeline.ParseOptions{
			Limits:      model.DefaultLimits(),
			ReordWindow: 8,
		},
		MinQuality:  *minQuality,
		MaxBitrate:  *maxBitrate,
		TargetBPS:   *targetBPS,
		BudgetMS:    *budgetMS,
		MaxDriftPPM: *maxDriftPPM,
	}
	in, err := pipeline.Parse(wavBuf, rtpBuf, mp4Buf, opts.ParseOptions)
	if err != nil {
		// Structural/parse failure: report and exit 1.
		fmt.Fprintf(os.Stderr, `{"error": %q, "exit_code": %d}`+"\n",
			err.Error(), report.ExitParse)
		return report.ExitParse
	}
	a := pipeline.Analyze(in, opts)
	v := pipeline.Validate(in, a)
	s := report.Build(in, a, v)
	if err := report.Write(os.Stdout, s); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return report.ExitParse
	}
	return s.ExitCode
}
