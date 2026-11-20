package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"jitterlab/internal/jitter"
	"jitterlab/internal/metrics"
	"jitterlab/internal/receiver"
	"jitterlab/internal/report"
)

type listenFlags struct {
	addr     string
	duration time.Duration
	jsonPath string
	quiet    bool
}

func runListen(args []string, stdout, stderr io.Writer) int {
	var f listenFlags
	fs := flag.NewFlagSet("listen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&f.addr, "addr", "127.0.0.1:42000", "loopback UDP address to bind")
	fs.DurationVar(&f.duration, "duration", 10*time.Second, "receive duration; zero runs until interrupted")
	fs.StringVar(&f.jsonPath, "json", "", "write a JSON report to this path")
	fs.BoolVar(&f.quiet, "quiet", false, "suppress the terminal summary")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		fmt.Fprintf(stderr, "listen: %v\n", err)
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "listen: unexpected arguments: %v\n", fs.Args())
		return exitUsage
	}
	if f.duration < 0 {
		fmt.Fprintf(stderr, "listen: duration must not be negative\n")
		return exitUsage
	}
	if f.duration > 24*time.Hour {
		fmt.Fprintf(stderr, "listen: duration exceeds 24h limit\n")
		return exitUsage
	}
	if _, err := net.ResolveUDPAddr("udp", f.addr); err != nil {
		fmt.Fprintf(stderr, "listen: invalid addr %q: %v\n", f.addr, err)
		return exitUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ctrl := jitter.NewController(jitter.DefaultConfig(), metrics.New())
	res, err := receiver.Listen(ctx, receiver.UDPConfig{Addr: f.addr, Duration: f.duration}, ctrl)
	if err == nil {
		// Emit the buffered tail before finalising so frames received at the
		// end of the session are not silently discarded.
		res.Elapsed += ctrl.DrainTail(2 * time.Second)
	}
	ctrl.Finalize()
	if err != nil {
		fmt.Fprintf(stderr, "listen: %v\n", err)
		return exitRuntime
	}

	rep := report.New(report.Run{Mode: "listen", DurationMS: res.Elapsed.Milliseconds()}, ctrl.Summary())
	if f.jsonPath != "" {
		if err := rep.WriteJSON(f.jsonPath); err != nil {
			fmt.Fprintf(stderr, "listen: write report: %v\n", err)
			return exitRuntime
		}
	}
	if !f.quiet {
		rep.Print(stdout)
		fmt.Fprintf(stdout, "datagrams_received=%d\n", res.Datagrams)
		fmt.Fprintf(stdout, "shutdown=%s\n", res.Shutdown)
	}
	return exitOK
}
