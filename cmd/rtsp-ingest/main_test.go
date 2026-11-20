package main

import (
	"context"
	"errors"
	"testing"

	"rtspingest/internal/cli"
	"rtspingest/internal/fsm"
	"rtspingest/internal/report"
)

func TestVerifyExpectationsControlPlaneFailure(t *testing.T) {
	// wrong transition count maps to the RTSP/state-machine code (3)
	line := report.Line{
		Transport:         "UDP",
		FinalState:        fsm.StateRecording.String(),
		StateTransitions:  3,
		RTPSeqGaps:        1,
		ReassembledFrames: 2,
		DiscardedFrames:   1,
		DuplicatePackets:  1,
	}
	code, err := verifyExpectations(line)
	if err == nil {
		t.Fatal("expected a control-plane failure")
	}
	if code != cli.ExitRTSPFailure {
		t.Fatalf("code = %d, want %d", code, cli.ExitRTSPFailure)
	}
}

func TestVerifyExpectationsMediaFailure(t *testing.T) {
	// control plane is fine but the media counters diverge -> code 4
	line := report.Line{
		Transport:         "TCP",
		FinalState:        fsm.StateClosed.String(),
		StateTransitions:  5,
		RTPSeqGaps:        7,
		ReassembledFrames: 2,
		DiscardedFrames:   1,
		DuplicatePackets:  1,
	}
	code, err := verifyExpectations(line)
	if err == nil {
		t.Fatal("expected a media-counters failure")
	}
	if code != cli.ExitMediaInvalid {
		t.Fatalf("code = %d, want %d", code, cli.ExitMediaInvalid)
	}
}

func TestVerifyExpectationsHealthy(t *testing.T) {
	line := report.Line{
		Transport:         "UDP",
		FinalState:        fsm.StateClosed.String(),
		StateTransitions:  5,
		RTPSeqGaps:        1,
		ReassembledFrames: 2,
		DiscardedFrames:   1,
		DuplicatePackets:  1,
	}
	if code, err := verifyExpectations(line); err != nil || code != cli.ExitOK {
		t.Fatalf("healthy line rejected: code=%d err=%v", code, err)
	}
}

func TestClassifyExitCodes(t *testing.T) {
	if c := classify(context.DeadlineExceeded); c != cli.ExitTimeoutOrFatal {
		t.Fatalf("deadline code = %d, want %d", c, cli.ExitTimeoutOrFatal)
	}
	if c := classify(context.Canceled); c != cli.ExitTimeoutOrFatal {
		t.Fatalf("cancel code = %d, want %d", c, cli.ExitTimeoutOrFatal)
	}
	if c := classify(errors.New("rtsp boom")); c != cli.ExitRTSPFailure {
		t.Fatalf("protocol code = %d, want %d", c, cli.ExitRTSPFailure)
	}
}

func TestParseArgFailureCode(t *testing.T) {
	if _, err := cli.Parse(nil); err == nil {
		t.Fatal("expected argument error for empty args")
	}
	// the run() wrapper maps every Parse error to ExitArgs (2); assert the
	// constant is distinct from every other code.
	codes := map[int]bool{
		cli.ExitOK: true, cli.ExitArgs: true, cli.ExitRTSPFailure: true,
		cli.ExitMediaInvalid: true, cli.ExitTimeoutOrFatal: true,
	}
	if len(codes) != 5 {
		t.Fatal("exit codes must be five distinct values")
	}
}
