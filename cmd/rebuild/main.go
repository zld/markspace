// Command rebuild is the terminal entry point of the media rebuild
// kernel. It generates deterministic sample inputs, runs the staged
// pipeline with checkpointed resume, and prints a verification report.
//
// Usage:
//
//	rebuild demo   --out DIR [--seed N]
//	rebuild resume --dir DIR [--interrupt-after STAGE]
//
// Exit codes: 0 = all stages clean; 2 = recoverable diagnostics but
// artifacts complete; 1 = unrecoverable input/state error or interrupt.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"mediarebuild/internal/pipeline"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "demo":
		return cmdDemo(ctx, args[1:])
	case "resume":
		return cmdResume(ctx, args[1:])
	default:
		usage()
		return 1
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: rebuild demo --out DIR [--seed N]")
	fmt.Fprintln(os.Stderr, "       rebuild resume --dir DIR [--interrupt-after STAGE]")
}

func cmdDemo(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	out := fs.String("out", "", "output directory (required)")
	seed := fs.Int64("seed", 1, "deterministic sample seed")
	if err := fs.Parse(args); err != nil || *out == "" {
		usage()
		return 1
	}
	p, err := pipeline.New(pipeline.Config{
		Dir: *out, Seed: *seed, RegenerateInput: true,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return execute(ctx, p)
}

func cmdResume(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	dir := fs.String("dir", "", "existing run directory (required)")
	interrupt := fs.String("interrupt-after", "", "stop after this stage completes")
	if err := fs.Parse(args); err != nil || *dir == "" {
		usage()
		return 1
	}
	p, err := pipeline.New(pipeline.Config{
		Dir: *dir, InterruptAfter: *interrupt,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	// Simulate a crashed run: stages past the interrupt point are
	// considered never completed, so their checkpoints are dropped.
	if *interrupt != "" {
		if err := p.DropCheckpointsAfter(*interrupt); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	}
	return execute(ctx, p)
}

// execute runs the pipeline and prints the verification report.
func execute(ctx context.Context, p *pipeline.Pipeline) int {
	err := p.Run(ctx)
	if errors.Is(err, pipeline.ErrInterrupted) {
		printReport(p)
		fmt.Println("status: interrupted (checkpoints preserved)")
		return 1
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	printReport(p)
	diags := p.Res.AllDiags()
	if diags.HasFatal() {
		fmt.Println("status: FAILED (unrecoverable diagnostics)")
		return 1
	}
	if diags.Count() > 0 {
		fmt.Println("status: OK with recoverable diagnostics")
		return 2
	}
	fmt.Println("status: OK")
	return 0
}

func printReport(p *pipeline.Pipeline) {
	res := &p.Res
	fmt.Println("== stages ==")
	fmt.Printf("  computed: %v\n", orNone(p.RanStages))
	fmt.Printf("  resumed:  %v\n", orNone(p.LoadedStages))
	if res.RTP != nil {
		fmt.Println("== rtp streams ==")
		for _, s := range res.RTP.Streams {
			fmt.Printf("  %-14s packets=%d unique=%d dups=%d lost=%d\n",
				s.Name, s.Packets, s.Unique, s.Dups, s.Lost)
		}
	}
	if res.RTCP != nil {
		fmt.Println("== rtcp alignment ==")
		fmt.Printf("  alignment error: %d ns\n", res.RTCP.AlignmentErrorNanos)
	}
	fmt.Println("== diagnostics ==")
	diags := res.AllDiags()
	if diags.Count() == 0 {
		fmt.Println("  (none)")
	}
	for _, d := range diags {
		fmt.Printf("  [%s] %-18s offset=%d end=%d recoverable=%v %s\n",
			d.Stage, d.Code, d.Offset, d.EndOffset, d.Recoverable, d.Message)
	}
	if res.Media != nil {
		m := res.Media
		fmt.Println("== media ==")
		for i, h := range m.FrameHashes {
			fmt.Printf("  frame[%02d] sha256=%s\n", i, h)
		}
		fmt.Printf("  fragment sha256=%s\n", m.FragmentHash)
		fmt.Printf("  pixel mean=%.3f peak=%d\n", m.PixelMean, m.PixelPeak)
		fmt.Printf("  audio peak=%d (%.2f dBFS)\n", m.AudioPeak, m.AudioPeakDBFS)
	}
	if res.Merge != nil {
		fmt.Println("== manifest ==")
		fmt.Printf("  entries=%d sha256=%s\n", res.Merge.Entries, res.Merge.ManifestSHA256)
	}
}

func orNone(ss []string) any {
	if len(ss) == 0 {
		return "(none)"
	}
	return ss
}
