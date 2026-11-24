package pipeline

import (
	"testing"
	"time"
)

func BenchmarkFullRun30s(b *testing.B) {
	opt := Options{Seed: 7, Duration: 30 * time.Second, LossBurst: 4,
		Reorder: 3, CorruptIndex: 2, RetryLimit: 2}
	for i := 0; i < b.N; i++ {
		if _, err := Run(opt); err != nil {
			b.Fatal(err)
		}
	}
}

func Test30sRunLatency(t *testing.T) {
	start := time.Now()
	res, err := Run(Options{Seed: 7, Duration: 30 * time.Second, LossBurst: 4,
		Reorder: 3, CorruptIndex: 2, RetryLimit: 2})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("30s fixture processed in %v, p95=%.2fms recovery=%.2f",
		elapsed, res.Summary.P95LatencyMs, res.Summary.PacketRecoveryRate)
	if res.Summary.P95LatencyMs > 120 {
		t.Fatalf("p95 %v exceeds 120ms", res.Summary.P95LatencyMs)
	}
}
