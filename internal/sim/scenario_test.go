package sim

import (
	"testing"
	"time"

	"jbbench/internal/protocol"
)

func TestDeterministicReplay(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Frames = 500
	a, err := Generate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatalf("lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ArriveNS != b[i].ArriveNS || a[i].Kind != b[i].Kind {
			t.Fatalf("event %d differs between runs", i)
		}
	}
}

func TestImpairmentCoverage(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Frames = 3000
	cfg.Seed = 7
	cfg.CorruptRate = 0.03
	cfg.DuplicateRate = 0.05
	cfg.LossRate = 0.05
	cfg.LossBurst = 7
	events, err := Generate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	counts := CountByKind(events)
	for _, kind := range []EventKind{
		EventOK, EventLost, EventDuplicate, EventCorrupt,
		EventBadVer, EventBadLen, EventAmbiguous,
	} {
		if counts[kind] == 0 {
			t.Errorf("no event of kind %s generated; counts=%v", kind, counts)
		}
	}
	// Every non-lost, non-bad event must decode or fail with a classified
	// error; nothing should panic and checksum errors identify corruption.
	for _, e := range events {
		if e.Datagram == nil {
			continue
		}
		_, err := protocol.Decode(e.Datagram)
		switch e.Kind {
		case EventOK, EventDuplicate:
			if err != nil {
				t.Errorf("frame %d kind %s failed decode: %v",
					e.FrameNo, e.Kind, err)
			}
		case EventCorrupt:
			if err == nil {
				t.Errorf("corrupt frame %d decoded cleanly", e.FrameNo)
			}
		}
	}
}

func TestBurstLossRuns(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Frames = 2000
	cfg.LossRate = 0.1
	cfg.LossBurst = 7
	cfg.DuplicateRate = 0
	cfg.CorruptRate = 0
	cfg.ReorderWindow = 0
	events, err := Generate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	lostFrames := map[uint32]bool{}
	for _, e := range events {
		if e.Kind == EventLost {
			lostFrames[e.FrameNo] = true
		}
	}
	longest := 0
	for start := range lostFrames {
		run := 0
		for lostFrames[start+uint32(run)] {
			run++
		}
		if run > longest {
			longest = run
		}
	}
	if longest < 2 {
		t.Fatalf("expected burst losses, longest run = %d", longest)
	}
	if longest > cfg.LossBurst {
		t.Fatalf("burst %d exceeds configured max %d", longest, cfg.LossBurst)
	}
}

func TestArrivalOrderingAndJitter(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Frames = 400
	cfg.JitterNS = 10 * time.Millisecond
	events, err := Generate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(events); i++ {
		if events[i-1].ArriveNS > events[i].ArriveNS {
			t.Fatal("events not sorted by arrival time")
		}
	}
}

func TestValidation(t *testing.T) {
	bad := []Config{
		{Frames: 0},
		{Frames: 10, LossRate: -1},
		{Frames: 10, DuplicateRate: 2},
		{Frames: 10, ReorderWindow: -1},
		{Frames: 10, LossRate: 0.9, DuplicateRate: 0.9},
	}
	for i, cfg := range bad {
		if _, err := Generate(cfg); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}
