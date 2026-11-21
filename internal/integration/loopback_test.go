package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"looprelay/internal/cli"
	"looprelay/internal/report"
)

func demoOptions(t *testing.T, mutate func(*cli.Options)) cli.Options {
	t.Helper()
	opts := cli.Defaults()
	opts.Packets = 60
	opts.Interval = 5 * time.Millisecond
	opts.Timeout = 10 * time.Second
	if mutate != nil {
		mutate(&opts)
	}
	if err := opts.Validate(); err != nil {
		t.Fatalf("invalid fixture options: %v", err)
	}
	return opts
}

func TestLoopbackDemoProducesTwoPairsAndAllPackets(t *testing.T) {
	if testing.Short() {
		t.Skip("loopback negotiation takes a few seconds")
	}
	opts := demoOptions(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := Run(ctx, FromCLI(opts))
	if result.Err != nil {
		t.Fatalf("run failed: %v (report: %+v)", result.Err, result.Report)
	}
	if result.Exit != ExitOK {
		t.Fatalf("exit=%d", result.Exit)
	}
	rep := result.Report
	if rep.CandidatePairCount != 2 {
		t.Fatalf("candidate_pair_count=%d want 2", rep.CandidatePairCount)
	}
	if len(rep.Links) != 2 {
		t.Fatalf("links=%d", len(rep.Links))
	}
	for _, link := range rep.Links {
		if !link.Connected {
			t.Fatalf("link %s not connected", link.Name)
		}
		if link.LocalAddress != "127.0.0.1" && link.LocalAddress != "::1" {
			t.Fatalf("link %s local address %q is not loopback", link.Name, link.LocalAddress)
		}
		if link.RemoteAddress != "127.0.0.1" && link.RemoteAddress != "::1" {
			t.Fatalf("link %s remote address %q is not loopback", link.Name, link.RemoteAddress)
		}
	}
	if rep.GeneratedPackets != 60 {
		t.Fatalf("generated=%d", rep.GeneratedPackets)
	}
	if rep.ForwardedPackets != 60 || rep.ReceivedPackets != 60 {
		t.Fatalf("forwarded=%d received=%d want 60", rep.ForwardedPackets, rep.ReceivedPackets)
	}
	if rep.LostPackets != 0 {
		t.Fatalf("lost=%d", rep.LostPackets)
	}
	if rep.JitterMS.Status != "ok" {
		t.Fatalf("jitter status=%s", rep.JitterMS.Status)
	}
	if !rep.Passed() {
		t.Fatalf("report did not pass: %+v", rep.Checks)
	}
}

func TestLoopbackDemoHandlesPlannedDropsAndDelays(t *testing.T) {
	if testing.Short() {
		t.Skip("loopback negotiation takes a few seconds")
	}
	opts := demoOptions(t, func(o *cli.Options) {
		o.Packets = 120
		o.DropEvery = 37
		o.DelayEvery = 29
		o.Delay = 7 * time.Millisecond
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result := Run(ctx, FromCLI(opts))
	if result.Err != nil {
		t.Fatalf("run failed: %v", result.Err)
	}
	rep := result.Report
	wantSkipped := 120 / 37
	if rep.SkippedPackets != wantSkipped {
		t.Fatalf("skipped=%d want %d", rep.SkippedPackets, wantSkipped)
	}
	if int(rep.ForwardedPackets) != 120-wantSkipped {
		t.Fatalf("forwarded=%d want %d", rep.ForwardedPackets, 120-wantSkipped)
	}
	if rep.ReceivedPackets != 120-wantSkipped {
		t.Fatalf("received=%d", rep.ReceivedPackets)
	}
	if rep.LostPackets != 0 {
		t.Fatalf("planned skips must not be reported as loss: lost=%d", rep.LostPackets)
	}
	if rep.JitterMS.Max <= 1.0 {
		t.Fatalf("periodic delay should raise max jitter above 1ms: %+v", rep.JitterMS)
	}
	if !rep.Passed() {
		t.Fatalf("report did not pass: %+v", rep.Checks)
	}
}

func TestLoopbackInterruptClosesCleanly(t *testing.T) {
	if testing.Short() {
		t.Skip("loopback negotiation takes a few seconds")
	}
	opts := demoOptions(t, func(o *cli.Options) {
		o.Packets = 2000
		o.Interval = 5 * time.Millisecond
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result, 1)
	go func() { done <- Run(ctx, FromCLI(opts)) }()
	time.Sleep(1500 * time.Millisecond)
	cancel()
	select {
	case result := <-done:
		if result.Report.Status == report.StatusOK && result.Err == nil {
			t.Fatal("cancelled run must not report success")
		}
		if result.Exit != ExitInterrupted {
			t.Fatalf("exit=%d want %d", result.Exit, ExitInterrupted)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestRunReportSerializesToStableJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("loopback negotiation takes a few seconds")
	}
	opts := demoOptions(t, func(o *cli.Options) {
		o.DropEvery = 7
		o.DelayEvery = 11
		o.Delay = 3 * time.Millisecond
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := Run(ctx, FromCLI(opts))
	if result.Err != nil {
		t.Fatalf("run failed: %v", result.Err)
	}
	var buf bytes.Buffer
	if err := report.WriteJSON(&buf, result.Report); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, key := range []string{
		"candidate_pair_count", "generated_packets", "forwarded_packets",
		"received_packets", "lost_packets", "jitter_ms", "status", "checks",
	} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing %q in %s", key, buf.String())
		}
	}
	if !strings.Contains(buf.String(), `"mean"`) || !strings.Contains(buf.String(), `"p95"`) {
		t.Fatalf("jitter_ms lacks mean/p95: %s", buf.String())
	}
}

func TestLoopbackDemoOnIPv6Listener(t *testing.T) {
	if testing.Short() {
		t.Skip("loopback negotiation takes a few seconds")
	}
	opts := demoOptions(t, func(o *cli.Options) {
		o.Listen = "[::1]:0"
		o.Packets = 40
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := Run(ctx, FromCLI(opts))
	if result.Err != nil {
		t.Fatalf("run failed: %v", result.Err)
	}
	if !strings.HasPrefix(result.Report.Listen, "[::1]:") {
		t.Fatalf("listen=%q", result.Report.Listen)
	}
	if result.Report.CandidatePairCount != 2 {
		t.Fatalf("pairs=%d", result.Report.CandidatePairCount)
	}
}
