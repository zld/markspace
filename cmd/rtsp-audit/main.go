// rtsp-audit compares the RTSP lifecycle and RTP reassembly of a
// deterministic local stream over UDP and TCP-interleaved transports.
//
// Exit codes: 0 ok, 2 argument error, 3 protocol/timeout error,
// 4 internal error.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"rtsp-audit/internal/cli"
	"rtsp-audit/internal/report"
	"rtsp-audit/internal/rtsp"
	"rtsp-audit/internal/simulator"
)

const (
	exitOK       = 0
	exitArgs     = 2
	exitProtocol = 3
	exitInternal = 4
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := cli.Parse(os.Args[1:], os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rtsp-audit:", err)
		return exitArgs
	}

	modes := cfg.Modes()
	results := make([]report.ModeResult, 0, len(modes))
	var runErr error

	for _, m := range modes {
		res, err := runMode(cfg, m)
		results = append(results, res)
		if err != nil && runErr == nil {
			runErr = err
		}
	}

	summary := report.NewSummary(modes, results)
	if cfg.JSON {
		if err := report.PrintJSON(os.Stdout, summary); err != nil {
			fmt.Fprintln(os.Stderr, "rtsp-audit:", err)
			return exitInternal
		}
	} else {
		report.PrintText(os.Stdout, modes, summary)
	}

	if runErr != nil {
		fmt.Fprintln(os.Stderr, "rtsp-audit:", runErr)
		if errors.Is(runErr, rtsp.ErrExternalURL) {
			return exitArgs
		}
		if errors.Is(runErr, errInternal) {
			return exitInternal
		}
		return exitProtocol
	}
	return exitOK
}

var errInternal = errors.New("internal error")

// runMode executes one transport mode against a fresh simulator
// instance and always returns the collected counts.
func runMode(cfg cli.Config, mode string) (report.ModeResult, error) {
	packets := simulator.Generate(cfg.Scenario, cfg.Seed)
	srv, err := simulator.Start(packets)
	if err != nil {
		return report.ModeResult{}, fmt.Errorf("%w: %v", errInternal, err)
	}
	defer srv.Close()

	transport := rtsp.TransportUDP
	if mode == "tcp-interleaved" {
		transport = rtsp.TransportTCP
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	stats, err := rtsp.Run(ctx, srv.URL(), transport, 300*time.Millisecond, simulator.IsPaddingPayload)
	return report.FromStats(stats), err
}
