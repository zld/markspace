// Command rtspdiag runs a fully local RTSP/RTP diagnostic session against an
// in-process mock server and reports state transitions, sequence gaps, frame
// reassembly and parse errors.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"rtspdiag/internal/config"
	"rtspdiag/internal/fixture"
	"rtspdiag/internal/report"
	"rtspdiag/internal/rtp"
	"rtspdiag/internal/rtsp"
	"rtspdiag/internal/sim"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return config.ExitUsage
	}
	switch args[0] {
	case "run":
		return runDiagnostic(args[1:])
	case "-h", "--help", "help":
		usage(os.Stdout)
		return config.ExitOK
	default:
		fmt.Fprintf(os.Stderr, "rtspdiag: unknown subcommand %q\n", args[0])
		usage(os.Stderr)
		return config.ExitUsage
	}
}

func usage(w *os.File) {
	fmt.Fprintln(w, "usage: rtspdiag run --transport udp|tcp --seed N --window N --timeout DUR [--json]")
}

func runDiagnostic(args []string) int {
	cfg, err := config.Parse(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return config.ExitOK
		}
		fmt.Fprintf(os.Stderr, "rtspdiag: %v\n", err)
		return config.ExitUsage
	}

	start := time.Now()
	summary, err := execute(cfg)
	if summary == nil {
		summary = &report.Summary{Transport: string(cfg.Transport), Seed: cfg.Seed}
	}
	summary.DurationMS = report.DurationMS(time.Since(start))
	if err != nil {
		fmt.Fprintf(os.Stderr, "rtspdiag: run failed: %v\n", err)
		return config.ExitFailure
	}

	if cfg.JSONOutput {
		if err := summary.WriteJSON(os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "rtspdiag: %v\n", err)
			return config.ExitFailure
		}
	} else {
		_ = summary.WriteText(os.Stdout)
	}
	return config.ExitOK
}

// execute builds the mock server, drives one RTSP session through the adapter
// and pipeline, and assembles the summary. A forced disconnect is treated as
// an expected diagnostic event, not a fatal failure.
func execute(cfg *config.Config) (*report.Summary, error) {
	plan := fixture.DefaultPlan(5)
	items := fixture.Generate(cfg.Seed, plan)

	var malformed []byte
	for _, it := range items {
		if it.Kind == fixture.KindMalformed {
			malformed = it.Raw
		}
	}

	transport := sim.TransportTCP
	if cfg.Transport == config.TransportUDP {
		transport = sim.TransportUDP
	}
	// Stream the full fixture, then force a server-side disconnect so the
	// client cleanup and buffer reset paths are exercised without losing data.
	server, err := sim.Start(sim.Config{
		Transport:       transport,
		Items:           items,
		PacketInterval:  2 * time.Millisecond,
		DisconnectAtEnd: true,
	})
	if err != nil {
		return nil, err
	}
	defer server.Close()

	pipeline := rtp.NewPipeline(cfg.MaxPacketSize, cfg.Window, cfg.Timeout)
	adapter := rtsp.NewAdapter(rtsp.AdapterConfig{
		Transport:       cfg.Transport,
		URL:             server.Address(),
		Window:          cfg.Window,
		ReorderTimeout:  cfg.Timeout,
		ReadTimeout:     5 * time.Second,
		Pipeline:        pipeline,
		InjectMalformed: malformed,
	})

	// Background reorder timeout ticker.
	tickCtx, stopTicks := context.WithCancel(context.Background())
	go tickReorder(tickCtx, pipeline, cfg.Timeout/2)
	defer stopTicks()

	// Bound the whole session so a wedged server cannot hang the CLI.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	runErr := adapter.Run(ctx)
	stopTicks()
	// End of stream: register remaining gaps and close open frames.
	pipeline.Flush(time.Now())

	disconnected := runErr != nil
	sm := adapter.Session()
	st := pipeline.Stats()
	summary := &report.Summary{
		Transport:           string(cfg.Transport),
		Seed:                cfg.Seed,
		StateTransitions:    sm.TransitionCount(),
		RejectedTransitions: sm.RejectedCount(),
		SequenceGaps:        st.GapEvents,
		MissingPackets:      st.MissingPackets,
		DuplicatePackets:    st.Duplicates,
		LatePackets:         st.LatePackets,
		TimeoutFlushes:      st.TimeoutFlushes,
		ReassembledFrames:   st.CompleteFrames,
		IncompleteFrames:    st.IncompleteFrames,
		ParseErrors:         st.ParseErrors,
		Disconnected:        disconnected,
	}

	// A forced server disconnect is the documented scenario; only unexpected
	// setup failures before PLAY are fatal.
	if runErr != nil && sm.State() == rtsp.StateInit {
		return summary, runErr
	}
	return summary, nil
}

// tickReorder periodically applies the reorder timeout until the context ends.
func tickReorder(ctx context.Context, p *rtp.Pipeline, interval time.Duration) {
	if interval <= 0 {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			p.Tick(now)
		}
	}
}
