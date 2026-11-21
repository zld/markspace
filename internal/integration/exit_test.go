package integration

import (
	"context"
	"net"
	"testing"
	"time"

	"looprelay/internal/cli"
	"looprelay/internal/report"
)

func TestRunFailsWhenSignalingPortIsTaken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().(*net.TCPAddr)

	opts := cli.Defaults()
	opts.Listen = addr.String()
	opts.Packets = 5
	opts.Interval = 5 * time.Millisecond
	opts.Timeout = 2 * time.Second
	if err := opts.Validate(); err != nil {
		t.Fatal(err)
	}

	result := Run(context.Background(), FromCLI(opts))
	if result.Exit != ExitSignaling {
		t.Fatalf("exit=%d want %d", result.Exit, ExitSignaling)
	}
	if result.Err == nil {
		t.Fatal("expected an error")
	}
	if result.Report.Status != report.StatusFailed {
		t.Fatalf("status=%s", result.Report.Status)
	}
	if result.Report.CandidatePairCount != 0 {
		t.Fatalf("failed run must not report pairs: %d", result.Report.CandidatePairCount)
	}
}
