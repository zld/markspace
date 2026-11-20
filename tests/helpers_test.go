package tests

import (
	"testing"

	"jitterlab/internal/jitter"
	"jitterlab/internal/metrics"
	"jitterlab/internal/packet"
)

func mustPacket(t *testing.T, seq int, ts uint32, payload byte) []byte {
	t.Helper()
	p, err := packet.New(1, uint32(seq), ts, 1, true, []byte{payload})
	if err != nil {
		t.Fatalf("packet.New: %v", err)
	}
	wire, err := packet.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return wire
}

func newController(t *testing.T, cfg jitter.Config) (*jitter.Controller, *metrics.Summary) {
	t.Helper()
	m := metrics.New()
	ctrl := jitter.NewController(cfg, m)
	t.Cleanup(func() { ctrl.Finalize() })
	return ctrl, m
}

func mutate(in []byte, f func([]byte)) []byte {
	out := append([]byte(nil), in...)
	f(out)
	return out
}
