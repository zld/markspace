package align

import (
	"errors"
	"testing"

	"rtp-rebuild/internal/clock"
	"rtp-rebuild/internal/events"
	"rtp-rebuild/internal/fault"
	"rtp-rebuild/internal/rtp"
)

func pkt(seq uint16, ts clock.Tick) rtp.Packet {
	return rtp.Packet{SSRC: 7, Seq: seq, ExtSeq: int64(seq), TS: ts, RawTS: uint32(ts)}
}

func TestIntervalBoundary(t *testing.T) {
	// Exactly at the threshold is not a stall; one tick over is.
	pkts := []rtp.Packet{
		pkt(1, 0), pkt(2, 18000), pkt(3, 36001),
	}
	res, err := Run(pkts, &events.Log{}, Config{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Intervals) != 1 {
		t.Fatalf("intervals = %d, want 1", len(res.Intervals))
	}
	iv := res.Intervals[0]
	if iv.Gap != 18001 {
		t.Fatalf("gap = %d", iv.Gap)
	}
	if iv.StartSeq != 2 || iv.EndSeq != 3 {
		t.Fatalf("seqs = %d..%d", iv.StartSeq, iv.EndSeq)
	}
	if iv.StartTime != 18000 || iv.EndTime != 36001 {
		t.Fatalf("times = %d..%d", iv.StartTime, iv.EndTime)
	}
}

func TestNoIntervalForSmallGaps(t *testing.T) {
	pkts := []rtp.Packet{pkt(1, 0), pkt(2, 3000), pkt(3, 6000)}
	res, _ := Run(pkts, &events.Log{}, Config{})
	if len(res.Intervals) != 0 {
		t.Fatalf("intervals = %d, want 0", len(res.Intervals))
	}
}

func TestIntervalAssociatesEvents(t *testing.T) {
	pkts := []rtp.Packet{pkt(10, 0), pkt(11, 90000)}
	log := &events.Log{Events: []events.Event{
		{Kind: events.KindStall, HasRelative: true, Relative: 1000},
		{Kind: events.KindResume, HasRelative: true, Relative: 50000},
		{Kind: events.KindJoin, HasRelative: true, Relative: 200000},
	}}
	res, err := Run(pkts, log, Config{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Intervals) != 1 {
		t.Fatalf("intervals = %d", len(res.Intervals))
	}
	if len(res.Intervals[0].Events) != 2 {
		t.Fatalf("interval events = %v, want 2", res.Intervals[0].Events)
	}
	if res.Events[0].Interval != 0 || res.Events[1].Interval != 0 || res.Events[2].Interval != -1 {
		t.Fatalf("event intervals = %d %d %d", res.Events[0].Interval, res.Events[1].Interval, res.Events[2].Interval)
	}
}

func TestUnknownOffsetMarkedExplicitly(t *testing.T) {
	log := &events.Log{Events: []events.Event{
		{Kind: events.KindStall, WallMillis: 1700000000500},
		{Kind: events.KindResume, HasRelative: true, Relative: 900},
	}}
	res, err := Run(nil, log, Config{HasCaptureStart: false})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Unalignable != 1 {
		t.Fatalf("unalignable = %d, want 1", res.Unalignable)
	}
	if res.Events[0].Record.Alignable {
		t.Fatal("wall event must be unalignable without capture start")
	}
	if res.Events[0].Record.Reason == "" {
		t.Fatal("missing reason for unalignable event")
	}
	if !res.Events[1].Record.Alignable {
		t.Fatal("relative event must align")
	}
}

func TestWallClockAlignment(t *testing.T) {
	log := &events.Log{Events: []events.Event{
		{Kind: events.KindStall, WallMillis: 1700000000750},
	}}
	res, err := Run(nil, log, Config{CaptureStartMillis: 1700000000000, HasCaptureStart: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Unalignable != 0 {
		t.Fatalf("unalignable = %d", res.Unalignable)
	}
	if res.Events[0].Media != clock.Tick(750*90) {
		t.Fatalf("media = %d, want %d", res.Events[0].Media, 750*90)
	}
}

func TestOverflowEventDoesNotAbortOthers(t *testing.T) {
	log := &events.Log{Events: []events.Event{
		{Kind: events.KindStall, WallMillis: -1 << 62},
		{Kind: events.KindResume, HasRelative: true, Relative: 4500},
	}}
	res, err := Run(nil, log, Config{})
	if err != nil {
		t.Fatalf("Run should not fail: %v", err)
	}
	if res.Unalignable != 1 {
		t.Fatalf("unalignable = %d, want 1", res.Unalignable)
	}
	if res.Events[1].Record.Reason != "" {
		t.Fatalf("second event should align, reason = %q", res.Events[1].Record.Reason)
	}
}

func TestUnplaceablePacketExcluded(t *testing.T) {
	// A zero TS packet (overflow marker) next to a valid one must not create a
	// huge interval from noise; only monotone increases beyond the threshold
	// count.
	pkts := []rtp.Packet{pkt(1, 500000), pkt(2, 0)}
	res, _ := Run(pkts, &events.Log{}, Config{})
	if len(res.Intervals) != 0 {
		t.Fatalf("intervals = %d, want 0", len(res.Intervals))
	}
	suspect := pkt(3, 0)
	suspect.TSSuspect = true
	res, _ = Run([]rtp.Packet{pkt(1, 0), suspect, pkt(4, 200000)}, &events.Log{}, Config{})
	if len(res.Intervals) != 0 {
		t.Fatalf("intervals = %d, want 0 when a suspect packet sits between", len(res.Intervals))
	}
}

func TestCustomThreshold(t *testing.T) {
	pkts := []rtp.Packet{pkt(1, 0), pkt(2, 9001)}
	res, _ := Run(pkts, &events.Log{}, Config{ThresholdTicks: 9000})
	if len(res.Intervals) != 1 {
		t.Fatalf("intervals = %d, want 1", len(res.Intervals))
	}
	res, _ = Run(pkts, &events.Log{}, Config{ThresholdTicks: clock.Tick(100000)})
	if len(res.Intervals) != 0 {
		t.Fatalf("intervals = %d, want 0", len(res.Intervals))
	}
	if !errors.Is(ErrNoTimestamp, fault.ErrCorrupt) {
		t.Fatal("ErrNoTimestamp should wrap corrupt")
	}
}
