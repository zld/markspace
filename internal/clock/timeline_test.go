package clock

import (
	"testing"
	"time"
)

func TestUnwrap33BitWrap(t *testing.T) {
	prev := uint64(Mod33 - 100)
	u := Unwrap33(prev, true, 50)
	if !u.WrappedThis {
		t.Fatal("expected wraparound")
	}
	if u.Value != Mod33+50 {
		t.Fatalf("unwrapped=%d want %d", u.Value, Mod33+50)
	}
	ms := PTSToMS(u.Value)
	if ms <= PTSToMS(prev) {
		t.Fatalf("unwrapped ms %d should advance after wrap", ms)
	}
}

func TestTrackMapMonotonicAcrossWrap(t *testing.T) {
	m := &TrackMap{}
	ms1, _, _ := m.MapPTS(Mod33 - 3600)
	ms2, wrapped, nonMono := m.MapPTS(3600)
	if !wrapped {
		t.Fatal("wrap not reported")
	}
	if nonMono {
		t.Fatal("33-bit wrap should expand to forward time, not disorder")
	}
	if ms2 <= ms1 {
		t.Fatalf("timestamps not monotonic: %d -> %d", ms1, ms2)
	}
}

func TestPercentile95RejectsEmpty(t *testing.T) {
	if _, n, ok := Percentile95(nil); ok || n != 0 {
		t.Fatal("empty sample must not produce a pseudo P95")
	}
	p95, n, ok := Percentile95([]time.Duration{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond})
	if !ok || n != 3 || p95 != 3 {
		t.Fatalf("p95=%v n=%d ok=%v", p95, n, ok)
	}
}

func TestSyncErrorNearestTrackSample(t *testing.T) {
	video := []int64{0, 40, 80}
	audio := []int64{0, 20, 40, 60, 80}
	if d := SyncError(video, audio); d != 0 {
		t.Fatalf("sync error=%v want 0", d)
	}
	if d := SyncError(nil, audio); d != -1 {
		t.Fatalf("empty video sync=%v want -1", d)
	}
}
