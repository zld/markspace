package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"time"

	"jitterlab/internal/trace"
)

// runSend is a verification aid: it replays a synthetic trace to a local UDP
// listener so the listen path can be exercised without external tooling.
func runSend(args []string, stdout, stderr io.Writer) int {
	var (
		addr     string
		seed     int64
		duration time.Duration
		loss     float64
		reorder  float64
		dup      float64
		burst    int
		rate     time.Duration
	)
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&addr, "addr", "127.0.0.1:42000", "destination UDP address")
	fs.Int64Var(&seed, "seed", 42, "trace seed")
	fs.DurationVar(&duration, "duration", 5*time.Second, "trace duration")
	fs.Float64Var(&loss, "loss", 0.0, "packet loss probability")
	fs.Float64Var(&reorder, "reorder", 0.0, "reorder probability")
	fs.Float64Var(&dup, "duplicate", 0.0, "duplicate probability")
	fs.IntVar(&burst, "burst", 5, "maximum consecutive loss run")
	fs.DurationVar(&rate, "rate", 20*time.Millisecond, "inter-packet pacing")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		fmt.Fprintf(stderr, "send: %v\n", err)
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "send: unexpected arguments: %v\n", fs.Args())
		return exitUsage
	}
	if rate <= 0 || rate > time.Second {
		fmt.Fprintf(stderr, "send: rate must be in (0s,1s]\n")
		return exitUsage
	}
	if duration <= 0 || duration > 24*time.Hour {
		fmt.Fprintf(stderr, "send: duration must be in (0s,24h]\n")
		return exitUsage
	}
	dst, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		fmt.Fprintf(stderr, "send: invalid addr %q: %v\n", addr, err)
		return exitUsage
	}
	conn, err := net.DialUDP("udp", nil, dst)
	if err != nil {
		fmt.Fprintf(stderr, "send: dial: %v\n", err)
		return exitRuntime
	}
	defer conn.Close()

	cfg := trace.DefaultConfig()
	cfg.Seed = seed
	cfg.Duration = duration
	cfg.Loss = loss
	cfg.Reorder = reorder
	cfg.Duplicate = dup
	cfg.Burst = burst
	cfg.FrameMS = int(rate / time.Millisecond)
	tr := trace.Generate(cfg)
	start := time.Now()
	for _, d := range tr.Packets {
		if wait := time.Until(start.Add(d.At)); wait > 0 {
			time.Sleep(wait)
		}
		if _, err := conn.Write(d.Data); err != nil {
			fmt.Fprintf(stderr, "send: write: %v\n", err)
			return exitRuntime
		}
	}
	fmt.Fprintf(stdout, "packets_sent=%d\n", len(tr.Packets))
	return exitOK
}
