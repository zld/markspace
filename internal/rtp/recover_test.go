package rtp

import (
	"testing"

	"mediaforensics/internal/fixture"
)

func runRecovery(t *testing.T, stream *fixture.RTPStream) Stats {
	t.Helper()
	wire, err := fixture.MarshalPackets(stream)
	if err != nil {
		t.Fatal(err)
	}
	rec := NewRecoverer(DefaultWindow, 3750, 90000)
	for _, raw := range wire {
		_ = rec.Ingest(raw)
	}
	stats, _ := rec.Close()
	return stats
}

func TestDuplicatesNotCountedAsRecovery(t *testing.T) {
	cfg := fixture.DefaultRTPConfig(1, 200, 0.0)
	cfg.DuplicateRate = 0.2
	stream := fixture.GenerateRTP(cfg)
	if len(stream.Duplicates) == 0 {
		t.Fatal("test setup produced no duplicates")
	}
	stats := runRecovery(t, stream)
	if stats.Duplicates != len(stream.Duplicates) {
		t.Fatalf("duplicates = %d, want %d", stats.Duplicates, len(stream.Duplicates))
	}
	// Every duplicated sequence must appear as one received packet, never
	// inflating the recovered count via the repeated copy.
	if stats.Recovered != 0 {
		t.Fatalf("duplicates must not be recovered packets, got %d", stats.Recovered)
	}
	if stats.Dropped < stats.Duplicates {
		t.Fatal("duplicate packets must be dropped")
	}
}

func TestReorderedPacketsRecovered(t *testing.T) {
	cfg := fixture.DefaultRTPConfig(2, 300, 0.0)
	cfg.LateRate = 0.15
	stream := fixture.GenerateRTP(cfg)
	if len(stream.Late) == 0 {
		t.Fatal("test setup produced no late packets")
	}
	stats := runRecovery(t, stream)
	if stats.Recovered == 0 {
		t.Fatal("in-window reordered packets must be recovered")
	}
	if stats.P95LatencyMs <= 0 {
		t.Fatal("recovered packets must contribute latency samples")
	}
}

func TestWindowLossUnrecoverable(t *testing.T) {
	cfg := fixture.DefaultRTPConfig(3, 400, 0.0)
	stream := fixture.GenerateRTPWindowLoss(cfg, 10)
	stats := runRecovery(t, stream)
	if stats.Unrecoverable < 10 {
		t.Fatalf("window gap must be unrecoverable, got %d", stats.Unrecoverable)
	}
	if stats.RecoveredRate() >= 1.0 {
		t.Fatal("a stream with permanent loss cannot report full recovery")
	}
}

func TestRecoveredRateAccounting(t *testing.T) {
	cfg := fixture.DefaultRTPConfig(4, 500, 0.08)
	stream := fixture.GenerateRTP(cfg)
	stats := runRecovery(t, stream)
	got := stats.InOrder + stats.Recovered + stats.Unrecoverable
	if got != stats.Expected {
		t.Fatalf("accounting imbalance: %d+%d+%d=%d expected=%d",
			stats.InOrder, stats.Recovered, stats.Unrecoverable, got, stats.Expected)
	}
	if r := stats.RecoveredRate(); r <= 0 || r > 1.0 {
		t.Fatalf("recovery rate out of range: %f", r)
	}
}

func TestMalformedPacketDropped(t *testing.T) {
	rec := NewRecoverer(DefaultWindow, 3750, 90000)
	if err := rec.Ingest([]byte{0x01, 0x02}); err == nil {
		t.Fatal("a two-byte RTP packet must fail parsing")
	}
	stats, _ := rec.Close()
	if stats.Malformed != 1 || stats.Dropped != 1 {
		t.Fatalf("malformed accounting wrong: %+v", stats)
	}
	if len(rec.Errors()) == 0 {
		t.Fatal("malformed packet must produce a forensic error")
	}
}

func TestSequenceWrapHandled(t *testing.T) {
	cfg := fixture.RTPConfig{
		Seed: 9, Packets: 40, SSRC: 7, ClockRate: 90000,
		StartSeq: 65530, FrameStep: 3750, PayloadType: 96,
	}
	stream := fixture.GenerateRTP(cfg)
	stats := runRecovery(t, stream)
	// 40 sequences spanning the uint16 wrap must be one continuous span.
	if stats.Expected != 40 {
		t.Fatalf("wrap-around span = %d, want 40", stats.Expected)
	}
	if stats.Received != 40 {
		t.Fatalf("all unique packets received, got %d", stats.Received)
	}
}

func TestP95LatencyBoundedForReorder(t *testing.T) {
	cfg := fixture.DefaultRTPConfig(11, 1000, 0.08)
	stream := fixture.GenerateRTP(cfg)
	stats := runRecovery(t, stream)
	// The generator swaps each late packet with an immediate neighbor, so
	// recovery latency stays within a handful of packet slots.
	if stats.P95LatencyMs > 200.0 {
		t.Fatalf("p95 latency unexpectedly high: %f ms", stats.P95LatencyMs)
	}
}
