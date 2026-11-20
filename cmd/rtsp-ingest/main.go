// Command rtsp-ingest verifies a locally generated H.264-over-RTSP publish.
// It starts a loopback RTSP server and a deterministic publisher for each
// requested transport, then prints the control-plane and media statistics.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"rtspingest/internal/cli"
	"rtspingest/internal/fsm"
	"rtspingest/internal/report"
	"rtspingest/internal/server"
	"rtspingest/internal/sim"
	"rtspingest/internal/transport"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(argv []string, stdout, stderr *os.File) int {
	opts, err := cli.Parse(argv)
	if err != nil {
		fmt.Fprintf(stderr, "rtsp-ingest: %v\n", err)
		return cli.ExitArgs
	}

	failed := false
	for _, name := range opts.Transports() {
		line, code, err := runOne(name, opts)
		if err != nil {
			fmt.Fprintf(stderr, "rtsp-ingest: %s transport failed: %v\n", name, err)
			failed = true
			if code != cli.ExitOK {
				return code
			}
		}
		if err := report.WriteLine(stdout, line); err != nil {
			fmt.Fprintf(stderr, "rtsp-ingest: report failed: %v\n", err)
			return cli.ExitTimeoutOrFatal
		}
	}
	if failed {
		return cli.ExitMediaInvalid
	}
	return cli.ExitOK
}

// runOne executes a single end-to-end transport session and builds its
// report line. The returned code classifies any failure.
func runOne(name string, opts cli.Options) (report.Line, int, error) {
	fixture := sim.BuildFixture(opts.Seed)
	machine := fsm.NewMachine()

	var adapter transport.Adapter
	useUDP := name == "udp"
	if useUDP {
		adapter = transport.NewUDPAdapter()
	} else {
		adapter = transport.NewInterleavedAdapter()
	}

	runner, err := server.NewRunner(machine, adapter, server.Config{
		UseUDP:       useUDP,
		ReadTimeout:  opts.Timeout,
		WriteTimeout: opts.Timeout,
	})
	if err != nil {
		return report.Line{}, cli.ExitTimeoutOrFatal, err
	}
	defer runner.Close()

	target := runner.RTSPURL()
	if err := cli.ValidateTargetURL(target); err != nil {
		return report.Line{}, cli.ExitArgs, err
	}

	mode := sim.TransportTCP
	if useUDP {
		mode = sim.TransportUDP
	}
	publisher, err := sim.NewPublisher(target, mode, opts.Timeout)
	if err != nil {
		return report.Line{}, cli.ExitArgs, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()

	pubErr := make(chan error, 1)
	go func() { pubErr <- publisher.Publish(ctx, fixture) }()

	select {
	case <-runner.Handler().Done():
	case <-ctx.Done():
		return report.Line{Transport: adapter.Name()}, cli.ExitTimeoutOrFatal,
			errors.New("timed out waiting for RTSP session teardown")
	}

	if err := <-pubErr; err != nil {
		return report.Line{Transport: adapter.Name()}, classify(err), err
	}
	if err := runner.Handler().Err(); err != nil {
		return report.Line{Transport: adapter.Name()}, cli.ExitRTSPFailure, err
	}

	stats := adapter.Pipeline().Snapshot()
	line := report.Line{
		Transport:          adapter.Name(),
		FinalState:         machine.State().String(),
		StateTransitions:   machine.TransitionCount(),
		FSMIllegal:         machine.IllegalCount(),
		FSMDuplicateEvents: machine.DuplicateCount(),
		PacketsReceived:    stats.PacketsReceived,
		RTPSeqGaps:         stats.SeqGaps,
		DuplicatePackets:   stats.DuplicatePackets,
		LateOutOfWindow:    stats.LateOutOfWindow,
		ReassembledFrames:  stats.ReassembledFrames,
		DiscardedFrames:    stats.DiscardedFrames,
		MissingFragments:   stats.MissingFragments,
		IllegalFUHeaders:   stats.IllegalFUHeaders,
		OversizedFrames:    stats.OversizedFrames,
		MalformedPackets:   stats.MalformedPackets,
		CompleteFrames:     stats.CompleteFrames,
	}

	if code, err := verifyExpectations(line); err != nil {
		return line, code, err
	}
	return line, cli.ExitOK, nil
}

// verifyExpectations checks the deterministic fixture's known-good counters.
func verifyExpectations(line report.Line) (int, error) {
	if line.FinalState != fsm.StateClosed.String() || line.StateTransitions != 5 {
		return cli.ExitRTSPFailure, fmt.Errorf(
			"control plane mismatch: state=%s transitions=%d", line.FinalState, line.StateTransitions)
	}
	want := report.Line{
		RTPSeqGaps:        1,
		ReassembledFrames: 2,
		DiscardedFrames:   1,
		DuplicatePackets:  1,
	}
	if line.RTPSeqGaps != want.RTPSeqGaps ||
		line.ReassembledFrames != want.ReassembledFrames ||
		line.DiscardedFrames != want.DiscardedFrames ||
		line.DuplicatePackets != want.DuplicatePackets {
		return cli.ExitMediaInvalid, fmt.Errorf(
			"media counters mismatch: gaps=%d reassembled=%d discarded=%d duplicates=%d",
			line.RTPSeqGaps, line.ReassembledFrames, line.DiscardedFrames, line.DuplicatePackets)
	}
	return cli.ExitOK, nil
}

// classify maps a publisher error onto the documented exit codes.
func classify(err error) int {
	if errors.Is(err, context.DeadlineExceeded) {
		return cli.ExitTimeoutOrFatal
	}
	return cli.ExitRTSPFailure
}
