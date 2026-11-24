package timeline

import (
	"testing"

	"mediasync/internal/model"
)

func ms(v int64) model.Nanos { return model.Nanos(v) * model.Nanos(model.NanosPerMilli) }

func sample(track uint32, idx int64, ct model.Nanos) Sample {
	return Sample{Index: idx, TrackID: track, Kind: model.KindVideo, CorrectedTime: ct, OriginalTime: ct}
}

func TestWatermarkMonotonic(t *testing.T) {
	wm, err := NewWatermark(Config{AllowedLateness: ms(10), MaxSamples: 16})
	if err != nil {
		t.Fatal(err)
	}
	if wm.Advance(ms(100)) != true {
		t.Fatal("first advance should move")
	}
	if got := wm.Value(); got != ms(90) {
		t.Errorf("watermark = %d, want 90ms", got)
	}
	if wm.Advance(ms(50)) {
		t.Error("watermark must not move backwards")
	}
	if got := wm.Value(); got != ms(90) {
		t.Errorf("watermark changed on backward event: %d", got)
	}
	if !wm.Advance(ms(200)) {
		t.Error("forward event should advance")
	}
	if got := wm.Value(); got != ms(190) {
		t.Errorf("watermark = %d, want 190ms", got)
	}
}

func TestWatermarkBoundaryExactlyAtLateness(t *testing.T) {
	wm, _ := NewWatermark(Config{AllowedLateness: ms(10), MaxSamples: 16})
	wm.Advance(ms(100))
	if wm.IsLate(ms(90)) {
		t.Error("event exactly at the watermark must not be late")
	}
	if !wm.IsLate(model.Nanos(ms(90) - 1)) {
		t.Error("event one nanosecond before must be late")
	}
}

func TestNewWatermarkValidation(t *testing.T) {
	if _, err := NewWatermark(Config{AllowedLateness: -1, MaxSamples: 4}); err == nil {
		t.Error("negative lateness must be rejected")
	}
	if _, err := NewWatermark(Config{MaxSamples: 0}); err == nil {
		t.Error("zero MaxSamples must be rejected")
	}
}

func TestTimelineOrdersByTimestampNotArrival(t *testing.T) {
	tl, err := New(Config{AllowedLateness: ms(1000), MaxSamples: 16})
	if err != nil {
		t.Fatal(err)
	}
	// Variable frame intervals: 0, 33, 100, 110 ms. No fixed grid.
	times := []model.Nanos{ms(0), ms(33), ms(100), ms(110)}
	// Offer in deliberately shuffled order.
	for _, i := range []int{2, 0, 3, 1} {
		res := tl.Offer(sample(1, int64(i), times[i]))
		if res.Late {
			t.Fatalf("sample %d unexpectedly late", i)
		}
	}
	got := tl.Samples()
	if len(got) != 4 {
		t.Fatalf("samples = %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].CorrectedTime < got[i-1].CorrectedTime {
			t.Fatalf("samples not time ordered: %+v", got)
		}
	}
	if got[2].CorrectedTime != ms(100) || got[3].CorrectedTime != ms(110) {
		t.Errorf("timestamps altered: %+v", got)
	}
}

func TestTimelineRetainsBothTimes(t *testing.T) {
	tl, _ := New(Config{AllowedLateness: ms(0), MaxSamples: 8})
	s := sample(1, 0, ms(10))
	s.OriginalTime = ms(12)
	tl.Offer(s)
	got := tl.Samples()[0]
	if got.OriginalTime != ms(12) || got.CorrectedTime != ms(10) {
		t.Errorf("original/corrected not both retained: %+v", got)
	}
}

func TestOfferLateRejected(t *testing.T) {
	tl, _ := New(Config{AllowedLateness: ms(10), MaxSamples: 8})
	tl.Offer(sample(1, 0, ms(100)))
	res := tl.Offer(sample(1, 1, ms(0)))
	if !res.Late {
		t.Fatal("old sample should be late")
	}
	if got := len(tl.Samples()); got != 1 {
		t.Errorf("late sample was inserted, len=%d", got)
	}
	late, _, _ := tl.Stats()
	if late != 1 {
		t.Errorf("late counter = %d", late)
	}
}

func TestTimelineMaxSamplesEviction(t *testing.T) {
	tl, _ := New(Config{AllowedLateness: ms(1000), MaxSamples: 2})
	for i := 0; i < 4; i++ {
		res := tl.Offer(sample(1, int64(i), ms(int64(i)*10)))
		if i >= 2 && res.Evicted == 0 {
			t.Errorf("offer %d did not report eviction", i)
		}
	}
	if len(tl.Samples()) != 2 {
		t.Errorf("len = %d, want 2", len(tl.Samples()))
	}
}

func TestInsertIgnoresWatermark(t *testing.T) {
	tl, _ := New(Config{AllowedLateness: ms(10), MaxSamples: 8})
	tl.Offer(sample(1, 0, ms(100)))
	tl.Insert(sample(1, 1, ms(0)))
	got := tl.Samples()
	if len(got) != 2 || got[0].CorrectedTime != 0 {
		t.Errorf("Insert did not place the late sample: %+v", got)
	}
}

func TestRecomputeDigestIndependentOfOrder(t *testing.T) {
	a := []Sample{sample(1, 0, ms(0)), sample(1, 1, ms(20)), sample(2, 7, ms(20))}
	b := []Sample{a[2], a[0], a[1]}
	if RecomputeDigest(a) != RecomputeDigest(b) {
		t.Error("digest depends on arrival order")
	}
	diff := []Sample{a[0], a[1]}
	if RecomputeDigest(a) == RecomputeDigest(diff) {
		t.Error("digest does not cover all samples")
	}
}

func TestReplayFromAffectedOffset(t *testing.T) {
	r, err := NewReplay(8)
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < 5; i++ {
		r.Append(sample(1, i, ms(i*10)))
	}
	out, n := r.Replay(ms(20), []Sample{sample(1, 99, ms(15))})
	if n != 4 {
		t.Fatalf("replayed %d events, want 4", n)
	}
	var idx []int64
	for _, s := range out {
		idx = append(idx, s.Index)
	}
	// Canonical order mixes the late event in at its true timestamp.
	want := []int64{99, 2, 3, 4}
	for i := range want {
		if idx[i] != want[i] {
			t.Fatalf("replay order = %v, want %v", idx, want)
		}
	}
}

func TestReplayWindowBounded(t *testing.T) {
	r, _ := NewReplay(3)
	for i := int64(0); i < 10; i++ {
		r.Append(sample(1, i, ms(i)))
	}
	if r.Len() != 3 {
		t.Errorf("len = %d, want 3", r.Len())
	}
	if r.Dropped() != 7 {
		t.Errorf("dropped = %d, want 7", r.Dropped())
	}
	if r.Earliest(ms(0)) != 0 {
		t.Errorf("earliest should find the oldest retained event")
	}
}

func TestSnapshotRestore(t *testing.T) {
	tl, _ := New(Config{AllowedLateness: ms(10), MaxSamples: 8})
	tl.Offer(sample(1, 0, ms(100)))
	snap := tl.Snapshot()
	tl2, _ := New(Config{AllowedLateness: ms(10), MaxSamples: 8})
	tl2.Restore(snap)
	if tl2.Watermark() != tl.Watermark() {
		t.Errorf("watermark not restored")
	}
	if len(tl2.Samples()) != len(tl.Samples()) {
		t.Errorf("samples not restored")
	}
}
