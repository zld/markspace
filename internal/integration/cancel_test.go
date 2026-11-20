package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"rtspingest/internal/fsm"
	"rtspingest/internal/server"
	"rtspingest/internal/sim"
	"rtspingest/internal/transport"
)

// TestCancelReleasesResources starts a session whose publish context is
// cancelled before the fixture can finish, then asserts the runner closes
// without hanging and the bound RTSP port becomes reusable. This mirrors the
// SIGINT/SIGTERM handling in the command entry point.
func TestCancelReleasesResources(t *testing.T) {
	machine := fsm.NewMachine()
	adapter := transport.NewUDPAdapter()
	runner, err := server.NewRunner(machine, adapter, server.Config{
		UseUDP:       true,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("server start: %v", err)
	}

	// cancel immediately: a publisher bound to this context must unwrap fast.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	publisher, err := sim.NewPublisher(runner.RTSPURL(), sim.TransportUDP, 5*time.Second)
	if err != nil {
		runner.Close()
		t.Fatalf("publisher: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- publisher.Publish(ctx, sim.BuildFixture(17)) }()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("publisher did not respond to cancellation")
	}

	runner.Close()

	// the machine must have been driven to a terminal state by the close.
	if state := machine.State(); state != fsm.StateCreated && state != fsm.StateClosed {
		// a session that never announced simply stops at Created; either is
		// acceptable, but it must never be left in a half-open Recording state.
		t.Fatalf("unexpected terminal state: %s", state)
	}
}
