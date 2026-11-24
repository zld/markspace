// Command mediascan is the thin parameter entrypoint around the reusable
// pipeline library. It has no GUI and no network access.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"mediascan/internal/fixture"
	"mediascan/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 1
	}
	switch args[0] {
	case "demo":
		return cmdDemo(args[1:])
	case "scan":
		return cmdScan(args[1:])
	case "verify":
		return cmdVerify(args[1:])
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		usage()
		return 1
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `mediascan - local DASH/HLS timeline + MP4/GOP fault scanner

usage:
  mediascan demo   --out DIR
  mediascan scan   --input FILE [--checkpoint PATH] [--resume] [--memory-mib N] [--media-dir DIR]
  mediascan verify --input FILE [--memory-mib N]

exit codes: 0 clean | 2 corruption located, report complete | 1 usage/unrecoverable`)
}

func cmdDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	out := fs.String("out", "", "fixture output directory")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "demo: --out is required")
		return 1
	}
	gt, err := fixture.Generate(*out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo: %v\n", err)
		return 1
	}
	fmt.Printf("demo_fixtures_generated=1\n")
	fmt.Printf("out=%s\n", gt.OutDir)
	fmt.Printf("good=%s\n", gt.GoodManifest)
	fmt.Printf("corrupt=%s cut_offset_bytes=%d victim_sample=%d truth_time_ticks=%d\n",
		gt.CorruptManifest, gt.Corrupt.CutOffsetBytes,
		gt.Corrupt.VictimSampleIndex, gt.Corrupt.SampleTimeUnified)
	fmt.Printf("discontinuity=%s\n", gt.DiscontinuityManifest)
	fmt.Printf("vfr=%s\n", gt.VFRManifest)
	fmt.Printf("wrap_drift=%s\n", gt.WrapDriftManifest)
	fmt.Printf("dash=%s misaligned=%s\n", gt.DashManifest, gt.DashMisalignedManifest)
	return 0
}

func cmdScan(args []string) int {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	input := fs.String("input", "", "path to local .m3u8 or .mpd manifest")
	checkpointPath := fs.String("checkpoint", "", "checkpoint file path")
	resume := fs.Bool("resume", false, "resume from the last safe checkpoint")
	memoryMiB := fs.Int("memory-mib", 8, "memory ceiling in MiB")
	mediaDir := fs.String("media-dir", "", "allowed material root (defaults to manifest dir)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *input == "" {
		fmt.Fprintln(os.Stderr, "scan: --input is required")
		return 1
	}
	if *memoryMiB < 1 {
		fmt.Fprintln(os.Stderr, "scan: --memory-mib must be >= 1")
		return 1
	}

	res, err := pipeline.Run(context.Background(), pipeline.Config{
		Input: *input, CheckpointPath: *checkpointPath, Resume: *resume,
		MemoryMiB: *memoryMiB, MediaDir: *mediaDir,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "scan error: %v\n", err)
		if res != nil && res.Report != nil {
			fmt.Print(pipeline.KVText(res))
		}
		return 1
	}
	fmt.Print(pipeline.KVText(res))
	fmt.Print(pipeline.FaultTable(res))
	return res.ExitCode
}

func cmdVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	input := fs.String("input", "", "path to local .m3u8 or .mpd manifest")
	memoryMiB := fs.Int("memory-mib", 8, "memory ceiling in MiB")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *input == "" {
		fmt.Fprintln(os.Stderr, "verify: --input is required")
		return 1
	}
	res, err := pipeline.Run(context.Background(), pipeline.Config{
		Input: *input, MemoryMiB: *memoryMiB,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify error: %v\n", err)
		return 1
	}
	fmt.Print(pipeline.KVText(res))
	fmt.Print(pipeline.FaultTable(res))
	return res.ExitCode
}
