// Command jitter validates voice-frame jitter buffering under weak-network
// conditions. Subcommands:
//
//	jitter demo   --path both --seed 41 --frames 600 ...   reproducible simulation
//	jitter listen --port 5005                              live loopback UDP receiver
//
// Exit codes: 0 success, 1 runtime/network error, 2 parameter error.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"jitter/internal/buffer"
	"jitter/internal/clock"
	"jitter/internal/demo"
	"jitter/internal/ingress"
	"jitter/internal/metrics"
	"jitter/internal/plc"
	"jitter/internal/wire"
)

const (
	exitOK    = 0
	exitRun   = 1
	exitUsage = 2
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "demo":
		return demoCmd(args[1:], stdout, stderr)
	case "listen":
		return listenCmd(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown subcommand %q\n", args[0])
		usage(stderr)
		return exitUsage
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `usage:
  jitter demo   --path both|udp|trace --seed N --frames N [--loss P] [--reorder P] [--duplicate P] [--waterline-every-ms N] [--csv-out FILE]
  jitter listen --port N [--waterline-every-ms N]`)
}

func demoCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var p demo.Params
	fs.StringVar(&p.Path, "path", "both", "receive path: udp, trace or both")
	fs.Int64Var(&p.Seed, "seed", 1, "trace RNG seed")
	fs.IntVar(&p.Frames, "frames", 600, "frames to play per path")
	fs.Float64Var(&p.Loss, "loss", 0.04, "packet loss probability")
	fs.Float64Var(&p.Reorder, "reorder", 0.08, "reorder probability")
	fs.Float64Var(&p.Duplicate, "duplicate", 0.02, "duplicate probability")
	fs.IntVar(&p.WaterlineEveryMs, "waterline-every-ms", 200, "waterline sample interval")
	fs.StringVar(&p.CSVOut, "csv-out", "", "write waterline CSV to this file instead of stdout")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if err := p.Validate(); err != nil {
		fmt.Fprintln(stderr, "parameter error:", err)
		return exitUsage
	}
	if err := demo.Run(p, stdout); err != nil {
		fmt.Fprintln(stderr, "demo failed:", err)
		return exitRun
	}
	return exitOK
}

func listenCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("listen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	port := fs.Int("port", 5005, "loopback UDP port to bind")
	everyMs := fs.Int("waterline-every-ms", 200, "waterline sample interval")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *port < 0 || *port > 65535 || *everyMs <= 0 {
		fmt.Fprintln(stderr, "parameter error: bad --port or --waterline-every-ms")
		return exitUsage
	}

	clk := clock.NewReal()
	ctrl := buffer.New(clk)
	rx, err := ingress.ListenUDP(*port)
	if err != nil {
		fmt.Fprintln(stderr, "listen failed:", err)
		return exitRun
	}
	defer rx.Close()
	fmt.Fprintf(stderr, "listening on %s (Ctrl-C to stop and summarize)\n", rx.Addr())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- rx.Serve(ctrl, ctx.Done()) }()

	concealer := plc.New(wire.SamplesPerFrame)
	rec := metrics.NewRecorder(*everyMs)
	ticker := time.NewTicker(time.Duration(wire.FrameMs) * time.Millisecond)
	defer ticker.Stop()
	framesOut := 0
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case err := <-serveErr:
			if err != nil {
				fmt.Fprintln(stderr, "receive failed:", err)
				return exitRun
			}
			break loop
		case <-ticker.C:
			pkt, ok := ctrl.Poll()
			var pcm []int16
			if ok {
				pcm = wire.BytesToPCM(pkt.Payload)
			}
			concealer.Output(pcm, ok)
			framesOut++
			rec.Record("udp", clk.Now(), ctrl.Queued(), ctrl.Target())
		}
	}

	st := ctrl.Stats()
	sum := metrics.Summary{
		Path:              "udp",
		FramesOut:         framesOut,
		Received:          st.Received,
		Malformed:         rx.Malformed(),
		DropsDuplicate:    st.Duplicates,
		DropsLate:         st.Late,
		DropsOutOfWindow:  st.OutOfWindow,
		DropsQueueFull:    st.DroppedFull,
		ConcealedFrames:   concealer.Concealed,
		ContinuityOKRatio: concealer.ContinuityRatio(),
		MaxBoundaryJump:   concealer.MaxJump,
	}
	if err := rec.WriteCSV(stdout); err != nil {
		fmt.Fprintln(stderr, "write CSV failed:", err)
		return exitRun
	}
	if err := metrics.WriteSummaries(stdout, []metrics.Summary{sum}); err != nil {
		fmt.Fprintln(stderr, "write summary failed:", err)
		return exitRun
	}
	return exitOK
}
