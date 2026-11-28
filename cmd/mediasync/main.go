// Command mediasync is the terminal entry point for offline media
// validation. It has two subcommands:
//
//	mediasync demo   --out DIR   generate the four deterministic fixtures
//	mediasync verify --input MP4 --chunks BIN [--workers N] [--queue N]
//
// Exit codes follow the contract: 0 pass, 1 runtime/cancel, 2 invalid media.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mediasync/internal/fault"
	"mediasync/internal/fixture"
	"mediasync/internal/pipeline"
	"mediasync/internal/report"
	"mediasync/internal/verifier"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return report.ExitRuntime
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "-h", "--help", "help":
		usage()
		return report.ExitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", args[0])
		usage()
		return report.ExitRuntime
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mediasync - offline dialogue/subtitle/sfx validation

Usage:
  mediasync demo   --out DIR
  mediasync verify --input FILE.mp4 --chunks packets.bin [--workers N] [--queue N]

Exit codes: 0 pass, 1 runtime error/cancel, 2 media validation failure
`)
}

// withSignalContext cancels on Ctrl-C so cancellation reaches the pool,
// merger and output stages via the single context.
func withSignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	outDir := fs.String("out", "", "output directory for generated fixtures")
	if err := fs.Parse(args); err != nil {
		return report.ExitRuntime
	}
	if *outDir == "" {
		fmt.Fprintln(os.Stderr, "demo requires --out DIR")
		return report.ExitRuntime
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create output dir: %v\n", err)
		return report.ExitRuntime
	}

	ctx, cancel := withSignalContext()
	defer cancel()
	for _, sc := range fixture.BuildScenarios() {
		if err := ctx.Err(); err != nil {
			return report.ExitRuntime
		}
		mp4Path := fmt.Sprintf("%s/%s.mp4", *outDir, sc.Name)
		pktPath := fmt.Sprintf("%s/%s_packets.bin", *outDir, sc.Name)
		if err := writeFile(mp4Path, sc.MP4); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", mp4Path, err)
			return report.ExitRuntime
		}
		if err := writeFile(pktPath, sc.Packets); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", pktPath, err)
			return report.ExitRuntime
		}
	}
	// Stable alias used by the documented verify command.
	var okPackets []byte
	for _, sc := range fixture.BuildScenarios() {
		if sc.Name == fixture.NameOK {
			okPackets = sc.Packets
		}
	}
	if err := writeFile(fmt.Sprintf("%s/packets.bin", *outDir), okPackets); err != nil {
		fmt.Fprintf(os.Stderr, "write packets.bin: %v\n", err)
		return report.ExitRuntime
	}
	fmt.Printf("wrote 4 fixture pairs to %s (ok, wrap_drift, packet_loss, truncated)\n", *outDir)
	return report.ExitOK
}

func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	input := fs.String("input", "", "path to the MP4/MOV file")
	chunks := fs.String("chunks", "", "path to the framed RTP packet stream")
	workers := fs.Int("workers", 4, "worker count (capped at pipeline.MaxWorkers)")
	queue := fs.Int("queue", 8, "bounded queue capacity (capped at pipeline.MaxQueue)")
	if err := fs.Parse(args); err != nil {
		return report.ExitRuntime
	}
	if *input == "" || *chunks == "" {
		fmt.Fprintln(os.Stderr, "verify requires --input and --chunks")
		return report.ExitRuntime
	}

	ctx, cancel := withSignalContext()
	defer cancel()

	// The pair runs as one job through the bounded, cancelable pool so the
	// worker/queue limits and cancellation path are always exercised.
	processor := func(ctx context.Context, job pipeline.Job) (any, error) {
		return verifier.VerifyPairWith(ctx, *input, *chunks, verifier.DefaultDriftThresholdPPM)
	}
	results, err := pipeline.Run(ctx, *workers, *queue, processor,
		[]pipeline.Job{{Index: 0, Input: *input}})
	if err != nil {
		if f, ok := fault.AsFault(err); ok && f.Code == fault.ErrCanceled {
			fmt.Fprintf(os.Stderr, "canceled: %v\n", err)
			return report.ExitRuntime
		}
		fmt.Fprintf(os.Stderr, "runtime error: %v\n", err)
		return report.ExitRuntime
	}
	res := results[0]
	if res.Canceled {
		fmt.Fprintln(os.Stderr, "canceled before completion")
		return report.ExitRuntime
	}
	if res.Err != nil {
		fmt.Fprintf(os.Stderr, "runtime error: %v\n", res.Err)
		return report.ExitRuntime
	}
	outcome, ok := res.Output.(*verifier.Outcome)
	if !ok || outcome == nil {
		fmt.Fprintln(os.Stderr, "internal error: missing verification outcome")
		return report.ExitRuntime
	}

	latencyMS := float64(res.Latency) / float64(time.Millisecond)
	outcome.Metrics.LatencyP95MS = latencyMS
	rendered, err := outcome.Report.RenderWithLatency(latencyMS)
	if err != nil {
		fmt.Fprintf(os.Stderr, "render report: %v\n", err)
		return report.ExitRuntime
	}
	fmt.Println(string(rendered))
	fmt.Printf("recovery_rate: %.1f%%\n", outcome.Metrics.RecoveryRate())

	if outcome.Report.HasValidationFaults() {
		return report.ExitInvalid
	}
	return report.ExitOK
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
