package model

import (
	"errors"
	"testing"
	"time"
)

func TestTimebaseAndClockMap(t *testing.T) {
	tb, err := NewTimebase(90000)
	if err != nil {
		t.Fatal(err)
	}
	if got := tb.PTS(40 * time.Millisecond); got != 3600 {
		t.Fatalf("pts=%d want 3600", got)
	}
	if got := tb.Duration(3600); got != 40*time.Millisecond {
		t.Fatalf("duration=%v", got)
	}
	clock := ClockMap{SourceRate: 48000, Media: tb, StartOffset: 10 * time.Millisecond}
	// 480 source ticks = 10 ms = 900 media ticks, plus 900 offset ticks.
	if got := clock.Map(480); got != 1800 {
		t.Fatalf("mapped=%d want 1800", got)
	}
	sync := CheckAVSync(0, 3600, tb, 3600)
	if !sync.WithinFrame {
		t.Fatal("exactly one frame must be tolerated")
	}
	sync = CheckAVSync(0, 3601, tb, 3600)
	if sync.WithinFrame {
		t.Fatal("more than one frame must fail")
	}
}

func TestCollectorAndExitCode(t *testing.T) {
	c := &Collector{}
	c.Add(Fault{Kind: FaultBadSHA})
	c.Add(Fault{Kind: FaultLatePacket})
	if c.Count(FaultBadSHA) != 1 || c.Count(FaultLatePacket, FaultDuplicatePacket) != 1 {
		t.Fatal("fault counts wrong")
	}
	if !c.Has(FaultBadSHA) || c.All()[0].Kind != FaultBadSHA {
		t.Fatal("fault sorting/query wrong")
	}
	err := NewExitError(ExitSyncConfig, "bad")
	if code := ExitCode(err); code != 3 {
		t.Fatalf("code=%d", code)
	}
	if ExitCode(nil) != 0 {
		t.Fatal("nil must be success")
	}
	wrapped := errors.Join(err)
	if ExitCode(wrapped) != 3 {
		t.Fatalf("wrapped code=%d", ExitCode(wrapped))
	}
}
