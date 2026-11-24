package report

import "testing"

func TestPercentileNearestRank(t *testing.T) {
	vals := []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	cases := []struct {
		pct  float64
		want int64
	}{
		{0, 10}, {50, 50}, {95, 100}, {100, 100},
	}
	for _, c := range cases {
		if got := Percentile(vals, c.pct); got != c.want {
			t.Errorf("P%g = %d, want %d", c.pct, got, c.want)
		}
	}
	// Empty input is a defined zero, not a panic.
	if got := Percentile(nil, 95); got != 0 {
		t.Fatalf("empty percentile = %d, want 0", got)
	}
	// Input must not be mutated (defensive copy).
	orig := append([]int64(nil), vals...)
	Percentile(vals, 90)
	for i := range vals {
		if vals[i] != orig[i] {
			t.Fatal("Percentile mutated its input")
		}
	}
}

func TestSyncAndLatency(t *testing.T) {
	b := NewBuilder()
	// Worst mismatch is 12 ms.
	b.RecordSync(0, 5_000_000)
	b.RecordSync(100_000_000, 112_000_000)
	b.RecordSync(-3_000_000, 0) // 3 ms, absolute
	if got := b.SyncErrorMS(); got != 12 {
		t.Fatalf("sync error = %f, want 12", got)
	}

	for i := int64(1); i <= 100; i++ {
		b.RecordLatency(i*1_000_000, i*1_000_000) // 1..100 ms
	}
	p95 := b.P95LatencyMS()
	if p95 <= 0 || p95 > 100 {
		t.Fatalf("P95 out of finite range: %f", p95)
	}
	// Nearest rank 95 of 100 samples -> 95th value = 95 ms.
	if p95 != 95 {
		t.Fatalf("P95 = %f, want 95", p95)
	}
}

func TestEmptyBuilderIsZero(t *testing.T) {
	b := NewBuilder()
	if b.SyncErrorMS() != 0 || b.P95LatencyMS() != 0 || b.LatencyCount() != 0 {
		t.Fatal("empty builder must report zeros")
	}
}

func TestAssembleSummary(t *testing.T) {
	b := NewBuilder()
	b.RecordLatency(0, 2_000_000)
	b.RecordSync(0, 1_000_000)
	s := Assemble(Params{
		Builder: b, RecoveryRate: 0.875, BufferPeakBytes: 4096,
		LateRecomputed: 3, LateRejected: 1, MetadataConflicts: 2,
		ErrorCodes:   []string{"LATE_REJECTED", "META_UNPARSEABLE", "LATE_REJECTED"},
		TimelineHash: 123456789,
	})
	if s.SyncErrorMS != 1 || s.PacketRecoveryRate != 0.875 || s.P95LatencyMS != 2 {
		t.Fatalf("metric fields wrong: %+v", s)
	}
	if s.BufferPeakBytes != 4096 || s.LateRecomputed != 3 || s.LateRejected != 1 {
		t.Fatalf("counter fields wrong: %+v", s)
	}
	if s.TimelineHash != 123456789 || s.MetadataConflicts != 2 {
		t.Fatalf("identity fields wrong: %+v", s)
	}
	if len(s.ErrorCodes) != 2 || s.ErrorCodes[0] != "LATE_REJECTED" || s.ErrorCodes[1] != "META_UNPARSEABLE" {
		t.Fatalf("dedup/sort wrong: %v", s.ErrorCodes)
	}
}
