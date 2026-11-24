// Command mediarun is the terminal adapter for the local media processing
// kernel. It generates reproducible fixtures, runs the repair/index/verify
// pipeline with resumable journals, and re-verifies committed output.
//
// Exit codes: 0 success / thresholds pass; 2 input or boundary validation
// failure; 3 resume or output commit failure.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"mediarun/internal/fault"
	"mediarun/internal/fixture"
	"mediarun/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches one subcommand and maps errors to the contract exit codes.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		usage(stderr)
		return fault.ExitValidation
	}
	var err error
	switch args[0] {
	case "fixture":
		err = runFixture(args[1:], stdout)
	case "process":
		err = runProcess(args[1:], stdout)
	case "verify":
		err = runVerify(args[1:], stdout)
	case "-h", "--help", "help":
		usage(stdout)
		return fault.ExitOK
	default:
		fmt.Fprintf(stderr, "unknown subcommand %q\n", args[0])
		usage(stderr)
		return fault.ExitValidation
	}
	if err != nil {
		var fe *fault.Error
		if errors.As(err, &fe) {
			fmt.Fprintln(stderr, "ERROR:", fe.Error())
		} else {
			fmt.Fprintln(stderr, "ERROR:", err.Error())
		}
		return fault.ExitCode(err)
	}
	return fault.ExitOK
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `usage: mediarun <command> [flags]

commands:
  fixture  generate sample and corrupt fixtures
           --dir DIR
  process  repair timestamps, build random-access index, verify
           --input DIR --out DIR [--target-bitrate BPS] [--resume]
  verify   re-check a committed output against its input and thresholds
           --input DIR --out DIR`)
}

func runFixture(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("dir", "./tmp-fixture", "fixture output directory")
	if err := fs.Parse(args); err != nil {
		return fault.New(fault.StageInput, "bad fixture flags: "+err.Error())
	}
	if *dir == "" {
		return fault.New(fault.StageInput, "--dir must not be empty")
	}
	if err := fixture.Generate(*dir); err != nil {
		return err
	}
	fmt.Fprintf(out, "fixture generated at %s\n", *dir)
	fmt.Fprintln(out, "  dash representations: 2 (v1 ~1200000 bps, v2 ~400000 bps)")
	fmt.Fprintln(out, "  hls segments: 4 with one EXT-X-DISCONTINUITY")
	fmt.Fprintln(out, "  boundary cases: corrupt/truncated corrupt/zerots corrupt/dupts")
	return nil
}

func runProcess(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("process", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	input := fs.String("input", "./tmp-fixture", "input fixture directory")
	outDir := fs.String("out", "./tmp-out", "output directory")
	target := fs.Int64("target-bitrate", 1_200_000, "target bitrate in bps")
	resume := fs.Bool("resume", false, "reuse journaled verified stages")
	if err := fs.Parse(args); err != nil {
		return fault.New(fault.StageInput, "bad process flags: "+err.Error())
	}
	res, err := pipeline.Run(pipeline.Options{
		InputDir: *input, OutDir: *outDir,
		TargetBitrate: *target, Resume: *resume,
	})
	if err != nil {
		return err
	}
	printResult(out, res)
	return nil
}

func runVerify(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	input := fs.String("input", "./tmp-fixture", "input fixture directory")
	outDir := fs.String("out", "./tmp-out", "committed output directory")
	if err := fs.Parse(args); err != nil {
		return fault.New(fault.StageInput, "bad verify flags: "+err.Error())
	}
	res, err := pipeline.VerifyCommitted(*input, *outDir)
	if err != nil {
		return err
	}
	printResult(out, res)
	fmt.Fprintln(out, "verify: OK (input provenance, index digest and thresholds match)")
	return nil
}

// printResult emits both localized summary lines and stable key=value
// lines so humans and scripts can read the same run.
func printResult(w io.Writer, res *pipeline.Result) {
	m := res.Metrics
	hr := 0.0
	if m.RandomAccessQueries > 0 {
		hr = 100 * float64(m.RandomAccessHits) / float64(m.RandomAccessQueries)
	}
	fmt.Fprintf(w, "目标码率 / target_bitrate_bps=%d\n", m.TargetBitrate)
	fmt.Fprintf(w, "实际码率 / actual_bitrate_bps=%d\n", m.ActualBitrate)
	fmt.Fprintf(w, "码率偏差 / bitrate_deviation_pct=%.4f%% (阈值 5%%)\n", m.BitrateDeviationPct)
	fmt.Fprintf(w, "修复时间戳数 / repaired_timestamps=%d\n", m.RepairedTimestamps)
	fmt.Fprintf(w, "随机索引条目数 / random_index_entries=%d\n", m.IndexEntries)
	fmt.Fprintf(w, "样本总数 / sample_count=%d\n", m.SampleCount)
	fmt.Fprintf(w, "时长 / duration_ms=%d\n", m.Duration.Millis())
	fmt.Fprintf(w, "质量统计摘要 / quality_summary=%q\n", m.QualitySummary)
	fmt.Fprintf(w, "最大对齐误差 / max_alignment_error_ms=%d (阈值 20ms)\n", m.MaxAlignmentErrorMs)
	fmt.Fprintf(w, "随机访问命中 / random_access_hits=%d/%d hit_rate=%.1f%% (要求 100%%)\n",
		m.RandomAccessHits, m.RandomAccessQueries, hr)
	fmt.Fprintf(w, "复用阶段数 / reused_stages=%d\n", res.ReusedStages)
	fmt.Fprintf(w, "输出摘要 / output_digest=%s\n", res.OutputDigest)
}
