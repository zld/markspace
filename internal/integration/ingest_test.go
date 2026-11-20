// Package integration contains end-to-end tests that exercise the full
// chain: deterministic publisher -> gortsplib server -> transport adapter ->
// shared state machine -> RTP reorder / H264 reassembly -> counters.
package integration

import (
	"context"
	"testing"
	"time"

	"rtspingest/internal/cli"
	"rtspingest/internal/fsm"
	"rtspingest/internal/server"
	"rtspingest/internal/sim"
	"rtspingest/internal/transport"
)

const testTimeout = 8 * time.Second

// runSession wires one complete session for the given transport and waits
// for it to tear down. It returns the final machine state and stats.
func runSession(t *testing.T, useUDP bool) (*fsm.Machine, transport.Stats) {
	t.Helper()

	fixture := sim.BuildFixture(17)
	machine := fsm.NewMachine()

	var adapter transport.Adapter
	if useUDP {
		adapter = transport.NewUDPAdapter()
	} else {
		adapter = transport.NewInterleavedAdapter()
	}

	runner, err := server.NewRunner(machine, adapter, server.Config{
		UseUDP:       useUDP,
		ReadTimeout:  testTimeout,
		WriteTimeout: testTimeout,
	})
	if err != nil {
		t.Fatalf("server start: %v", err)
	}
	defer runner.Close()

	target := runner.RTSPURL()
	if err := cli.ValidateTargetURL(target); err != nil {
		t.Fatalf("target validation: %v", err)
	}

	mode := sim.TransportTCP
	if useUDP {
		mode = sim.TransportUDP
	}
	publisher, err := sim.NewPublisher(target, mode, testTimeout)
	if err != nil {
		t.Fatalf("publisher construction: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	pubErr := make(chan error, 1)
	go func() { pubErr <- publisher.Publish(ctx, fixture) }()

	select {
	case <-runner.Handler().Done():
	case <-ctx.Done():
		t.Fatal("timed out waiting for session teardown")
	}

	if err := <-pubErr; err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := runner.Handler().Err(); err != nil {
		t.Fatalf("server handler error: %v", err)
	}

	return machine, adapter.Pipeline().Snapshot()
}

func TestEndToEndUDP(t *testing.T) {
	machine, stats := runSession(t, true)
	assertAcceptance(t, "UDP", machine, stats)
}

func TestEndToEndTCP(t *testing.T) {
	machine, stats := runSession(t, false)
	assertAcceptance(t, "TCP", machine, stats)
}

// assertAcceptance enforces the exact counters from the task specification.
func assertAcceptance(t *testing.T, name string, machine *fsm.Machine, stats transport.Stats) {
	t.Helper()

	if machine.State() != fsm.StateClosed {
		t.Errorf("%s: final state = %s, want Closed", name, machine.State())
	}
	if got := machine.TransitionCount(); got != 5 {
		t.Errorf("%s: state_transitions = %d, want 5", name, got)
	}
	if got := machine.IllegalCount(); got != 0 {
		t.Errorf("%s: illegal transitions = %d, want 0", name, got)
	}
	if got := stats.SeqGaps; got != 1 {
		t.Errorf("%s: rtp_seq_gaps = %d, want 1", name, got)
	}
	if got := stats.DuplicatePackets; got != 1 {
		t.Errorf("%s: duplicate_packets = %d, want 1", name, got)
	}
	if got := stats.ReassembledFrames; got != 2 {
		t.Errorf("%s: reassembled_frames = %d, want 2", name, got)
	}
	if got := stats.DiscardedFrames; got != 1 {
		t.Errorf("%s: discarded_frames = %d, want 1", name, got)
	}
	if got := stats.CompleteFrames; got != 2 {
		t.Errorf("%s: complete_frames = %d, want 2", name, got)
	}
}

// TestTransportsCountIndependently runs both adapters in sequence and
// confirms each keeps its own counters rather than sharing any global state.
func TestTransportsCountIndependently(t *testing.T) {
	mUDP, sUDP := runSession(t, true)
	mTCP, sTCP := runSession(t, false)

	if mUDP.TransitionCount() != mTCP.TransitionCount() {
		t.Fatal("both machines must record the same transition count")
	}
	if sUDP.PacketsReceived == 0 || sTCP.PacketsReceived == 0 {
		t.Fatal("each transport must receive packets independently")
	}
	if sUDP.PacketsReceived != sTCP.PacketsReceived {
		t.Fatalf("packet counts differ: UDP=%d TCP=%d",
			sUDP.PacketsReceived, sTCP.PacketsReceived)
	}
}

// TestLoopbackOnlyTarget guards the address restriction helper.
func TestLoopbackOnlyTarget(t *testing.T) {
	good := []string{
		"rtsp://127.0.0.1:8554/demo",
		"rtsp://localhost:8554/demo",
	}
	for _, u := range good {
		if err := cli.ValidateTargetURL(u); err != nil {
			t.Errorf("expected %q accepted, got %v", u, err)
		}
	}

	bad := []string{
		"rtsp://10.0.0.1:8554/demo",
		"rtsp://192.168.1.5:8554/demo",
		"http://127.0.0.1:8554/demo",
		"rtsp://example.com/demo",
	}
	for _, u := range bad {
		if err := cli.ValidateTargetURL(u); err == nil {
			t.Errorf("expected %q rejected", u)
		}
	}
}
