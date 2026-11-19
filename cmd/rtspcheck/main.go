// Command rtspcheck validates a local, loopback-only mock RTSP camera
// source: it negotiates an RTSP session over interleaved TCP or UDP,
// reorders RTP packets, reassembles frames and emits a deterministic
// JSON or single-line summary report.
//
// Exit codes: 0 = session and report completed; 1 = negotiation,
// transport or processing failure; 2 = invalid command-line arguments.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"rtspcheck/internal/mock"
	"rtspcheck/internal/report"
	"rtspcheck/internal/session"
	"rtspcheck/internal/transport"
)

const (
	exitOK       = 0
	exitFailure  = 1
	exitUsage    = 2
	defaultPath  = "/cam"
	streamWindow = 15 * time.Second
)

type options struct {
	mode            string
	frames          int
	seed            int64
	dropEvery       int
	dupEvery        int
	reorderProb     float64
	delay           int
	reorderWindow   int
	format          string
	udpMinPort      int
	udpMaxPort      int
	failStage       string
	dropConn        bool
	maxFramePackets int
	maxFrameBuffer  int
}

func parseFlags(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("rtspcheck", flag.ContinueOnError)
	fs.StringVar(&o.mode, "mode", "tcp", "transport mode: tcp or udp")
	fs.IntVar(&o.frames, "frames", 120, "number of frames to generate and validate")
	fs.Int64Var(&o.seed, "seed", 1, "deterministic RNG seed")
	fs.IntVar(&o.dropEvery, "drop-every", 0, "drop 1 packet out of every N (0 disables)")
	fs.IntVar(&o.dupEvery, "duplicate-every", 0, "duplicate 1 packet out of every N (0 disables)")
	fs.Float64Var(&o.reorderProb, "reorder-prob", 0, "adjacent-packet swap probability 0..1")
	fs.IntVar(&o.delay, "delay", 0, "max per-packet jitter in microseconds")
	fs.IntVar(&o.reorderWindow, "reorder-window", 8, "RTP sequence reorder window")
	fs.StringVar(&o.format, "format", "json", "output format: json or summary")
	fs.IntVar(&o.udpMinPort, "udp-min-port", 30000, "minimum client UDP port")
	fs.IntVar(&o.udpMaxPort, "udp-max-port", 30200, "maximum client UDP port")
	fs.StringVar(&o.failStage, "fail-stage", "", "inject a non-2xx response at describe|setup|play")
	fs.BoolVar(&o.dropConn, "drop-conn", false, "close the connection immediately after PLAY")
	fs.IntVar(&o.maxFramePackets, "max-frame-packets", 512, "packets allowed per frame")
	fs.IntVar(&o.maxFrameBuffer, "max-buffered-frames", 256, "concurrently buffered frames")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if err := o.validate(); err != nil {
		return o, err
	}
	return o, nil
}

func (o options) validate() error {
	if _, err := transport.ParseMode(o.mode); err != nil {
		return err
	}
	if _, err := report.ParseFormat(o.format); err != nil {
		return err
	}
	if o.frames <= 0 {
		return fmt.Errorf("--frames must be positive, got %d", o.frames)
	}
	if o.reorderWindow < 1 || o.reorderWindow > 4096 {
		return fmt.Errorf("--reorder-window must be in 1..4096, got %d", o.reorderWindow)
	}
	if o.dropEvery < 0 || o.dupEvery < 0 || o.delay < 0 {
		return fmt.Errorf("drop/duplicate/delay values must be non-negative")
	}
	if o.reorderProb < 0 || o.reorderProb > 1 {
		return fmt.Errorf("--reorder-prob must be between 0 and 1")
	}
	switch o.failStage {
	case "", "describe", "setup", "play":
	default:
		return fmt.Errorf("--fail-stage must be describe, setup or play")
	}
	return nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	o, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, "rtspcheck:", err)
		return exitUsage
	}

	mode, _ := transport.ParseMode(o.mode)
	format, _ := report.ParseFormat(o.format)

	// In-process loopback mock source; no external process or device.
	srvCfg := mock.ServerConfig{
		FrameCount: o.frames,
		Fault: mock.FaultConfig{
			Seed:           o.seed,
			DropEvery:      o.dropEvery,
			DuplicateEvery: o.dupEvery,
			Reorder:        o.reorderProb,
			Delay:          o.delay,
		},
		DropConnAfterPlay: o.dropConn,
	}
	if o.failStage != "" {
		srvCfg.FailStage = mock.Stage(o.failStage)
		srvCfg.FailStatus = 500
	}

	// Distinct, non-overlapping server UDP ports leave the client's
	// configured range free for its own sockets.
	srv, err := mock.NewServer(srvCfg, 31000, 31001)
	if err != nil {
		fmt.Fprintln(stderr, "rtspcheck:", err)
		return exitFailure
	}
	if err := srv.Start(); err != nil {
		fmt.Fprintln(stderr, "rtspcheck:", err)
		return exitFailure
	}
	defer srv.Close()

	runner, err := session.NewRunner(session.Config{
		Mode:              mode,
		URL:               "rtsp://" + srv.RTSPAddress() + defaultPath,
		ReorderWindow:     o.reorderWindow,
		MaxFramePackets:   o.maxFramePackets,
		MaxBufferedFrames: o.maxFrameBuffer,
		StreamTimeout:     streamWindow,
		// Also used as the gortsplib read timeout: keep it above the
		// whole stream duration so it does not fire mid-receive.
		NegotiationTimeout: 10 * time.Second,
		UDPMinPort:         o.udpMinPort,
		UDPMaxPort:         o.udpMaxPort,
		Seed:               o.seed,
		FramesRequested:    o.frames,
		ExpectedPackets:    srv.ScheduledPackets(),
		SentCh:             srv.Sent(),
	})
	if err != nil {
		fmt.Fprintln(stderr, "rtspcheck:", err)
		return exitUsage
	}

	rep, runErr := runner.Run(context.Background())
	if werr := rep.Write(stdout, format); werr != nil {
		fmt.Fprintln(stderr, "rtspcheck:", werr)
		return exitFailure
	}
	if runErr != nil {
		fmt.Fprintln(stderr, "rtspcheck: session failed:", runErr)
		return exitFailure
	}
	return exitOK
}
