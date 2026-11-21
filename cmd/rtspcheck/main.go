// rtspcheck exercises a local mock RTSP server over UDP and TCP
// interleaved transports and reports deterministic RTP/H.264 statistics.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"time"

	"rtspcheck/internal/config"
	"rtspcheck/internal/demo"
	"rtspcheck/internal/report"
	"rtspcheck/internal/rtsp"
	"rtspcheck/internal/transport"
)

func main() {
	// gortsplib emits informational "packets lost"/decode lines through the
	// standard logger; they are intentionally injected conditions here and
	// already surfaced as structured counters, so silence that noise to keep
	// stdout/stderr to the documented contract.
	log.SetOutput(io.Discard)
	os.Exit(run(os.Args))
}

func run(args []string) int {
	flags, err := config.Parse(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parameter error: "+err.Error())
		return report.ExitConfigError
	}
	if flags.Command != "demo" {
		fmt.Fprintf(os.Stderr, "parameter error: unknown command %q\n", flags.Command)
		return report.ExitConfigError
	}
	return runDemo(flags)
}

// runDemo starts one mock server, plays the same manifest over every
// requested transport and maps the outcome to the documented exit codes.
func runDemo(flags *config.Flags) int {
	manifest := demo.Generate(flags.Frames, flags.Seed)

	mock := rtsp.NewMockServer(mapFault(flags.Fault), packetPace(flags))
	if err := mock.Start(); err != nil {
		fmt.Fprintln(os.Stderr, report.FormatError("server", err))
		return report.ExitRTSPFailure
	}
	defer mock.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// The server streams the same manifest once; each transport run needs
	// its own emission, so the server restarts its sender per run through
	// the waitForPlayer gate.
	var lines []report.Line
	worst := report.ExitOK

	for _, tr := range flags.Transports() {
		sum := runOne(ctx, mock, manifest, tr, flags.Timeout)
		if sum.Err != nil {
			// A failed RTSP stage produced no stream, so emit no JSON line;
			// the stage error on stderr and exit code carry the diagnosis.
			fmt.Fprintln(os.Stderr, report.FormatError(sum.Stage, sum.Err))
			worst = maxExit(worst, report.ExitRTSPFailure)
			continue
		}
		lines = append(lines, report.FromSummary(
			sum.Transport,
			sum.StateTransitions,
			sum.RTPSeqGaps,
			sum.FramesReassembled,
			sum.FramesDropped,
			sum.DuplicatePackets,
		))
	}

	// If every transport failed there is no successful summary to render.
	if len(lines) == 0 {
		return worst
	}
	if err := report.WriteLines(os.Stdout, lines); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return report.ExitPacketFailure
	}
	return worst
}

// runOne starts the manifest sender for a single transport and executes the
// matching client link. The sender is scoped to this call so UDP and TCP
// observe byte-identical packet sequences.
func runOne(
	parent context.Context,
	mock *rtsp.MockServer,
	manifest *demo.Manifest,
	tr string,
	timeout time.Duration,
) *transport.Summary {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	streamDone := make(chan struct{})
	senderErr := make(chan error, 1)
	go func() {
		senderErr <- mock.Stream(ctx, manifest)
		close(streamDone)
	}()

	sum := transport.Run(ctx, mock.URL(), transport.Options{
		Transport:       tr,
		Timeout:         timeout,
		StreamDone:      streamDone,
		MaxPayload:      config.MaxPacketPayload,
		KnownGaps:       manifest.ExpectedGaps,
		KnownDuplicates: manifest.ExpectedDuplicates,
	})

	// Stop the sender if the runner returned before it finished (failures).
	cancel()
	select {
	case <-senderErr:
	case <-time.After(500 * time.Millisecond):
	}
	return sum
}

// packetPace spreads packets so interleaved TCP and UDP delivery stays
// orderly while keeping the whole demo well under the timeout.
func packetPace(flags *config.Flags) time.Duration {
	return time.Millisecond
}

// mapFault translates the validated CLI fault name to the mock's mode.
func mapFault(name string) rtsp.FaultMode {
	switch name {
	case config.FaultBadSDP:
		return rtsp.FaultBadSDP
	case config.FaultSetupFail:
		return rtsp.FaultSetupRejected
	default:
		return rtsp.FaultNone
	}
}

func maxExit(a, b int) int {
	if a > b {
		return a
	}
	return b
}
