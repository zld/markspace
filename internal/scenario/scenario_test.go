package scenario

import (
	"testing"
	"time"

	"jitterlab/internal/protocol"
)

func testCfg() Config {
	return Config{
		Frames: 300, Seed: 41, StreamID: 7,
		LossBurst: 7, ReorderDepth: 9, DuplicateRate: 0.05,
		JitterMs: 18, DriftPPM: 120,
		FrameInterval: 20 * time.Millisecond, PayloadBytes: 64,
	}
}

func TestGenerateDeterministic(t *testing.T) {
	a, b := Generate(testCfg()), Generate(testCfg())
	if len(a) != len(b) || len(a) == 0 {
		t.Fatalf("len %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].At != b[i].At || string(a[i].Data) != string(b[i].Data) {
			t.Fatalf("event %d differs", i)
		}
	}
}

func TestGenerateSortedAndDecodable(t *testing.T) {
	evs := Generate(testCfg())
	for i := 1; i < len(evs); i++ {
		if evs[i].At < evs[i-1].At {
			t.Fatalf("not sorted at %d", i)
		}
	}
	seen := make(map[uint32]int)
	for _, e := range evs {
		p, err := protocol.Decode(e.Data, 0)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		seen[p.Seq]++
	}
	dups, received := 0, 0
	for _, c := range seen {
		received++
		if c > 1 {
			dups++
		}
	}
	if dups == 0 {
		t.Fatal("duplicate rate > 0 but no duplicates generated")
	}
	if received >= 300 {
		t.Fatal("loss burst configured but nothing was lost")
	}
}

func TestGenerateLossGuaranteed(t *testing.T) {
	cfg := testCfg()
	cfg.DuplicateRate = 0
	cfg.Seed = 1 // any seed must still produce the guaranteed burst
	evs := Generate(cfg)
	if len(evs) >= cfg.Frames {
		t.Fatalf("expected losses, got %d of %d", len(evs), cfg.Frames)
	}
}

func TestGenerateEmpty(t *testing.T) {
	if evs := Generate(Config{Frames: 0}); evs != nil {
		t.Fatal("zero frames must yield no events")
	}
}
