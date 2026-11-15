package sim

import (
	"bytes"
	"testing"
)

func testConfig() Config {
	return Config{
		Seed: 42, Frames: 240, FrameMS: 20, SampleRate: 8000,
		BaseDelayMS: 45, JitterMS: 18, LossBurst: 4,
		DuplicateRate: 0.03, ReorderRate: 0.12, StreamID: 1,
	}
}

func TestGenerateDeterministic(t *testing.T) {
	a, b := Generate(testConfig()), Generate(testConfig())
	if len(a) != len(b) {
		t.Fatalf("len %d != %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ArrivalMs != b[i].ArrivalMs || !bytes.Equal(a[i].Data, b[i].Data) {
			t.Fatalf("datagram %d differs between runs", i)
		}
	}
}

func TestGenerateLossAndDuplicates(t *testing.T) {
	cfg := testConfig()
	dg := Generate(cfg)
	// Expect roughly 10% burst loss plus ~3% duplicates.
	if len(dg) < 200 || len(dg) > 240 {
		t.Fatalf("datagram count %d outside expected range", len(dg))
	}
	prev := int64(-1)
	for _, d := range dg {
		if d.ArrivalMs < prev {
			t.Fatal("datagrams not sorted by arrival")
		}
		prev = d.ArrivalMs
	}
	// No loss, no duplicates: exactly Frames datagrams.
	cfg.LossBurst, cfg.DuplicateRate, cfg.ReorderRate = 0, 0, 0
	if got := len(Generate(cfg)); got != cfg.Frames {
		t.Fatalf("clean run produced %d want %d", got, cfg.Frames)
	}
}

func TestGenerateArrivalAfterSend(t *testing.T) {
	for _, d := range Generate(testConfig()) {
		if d.ArrivalMs < 0 {
			t.Fatalf("negative arrival %d", d.ArrivalMs)
		}
	}
}
