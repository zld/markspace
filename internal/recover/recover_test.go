package recover

import (
	"bytes"
	"testing"

	"mediad/internal/cache"
	"mediad/internal/model"
	"mediad/internal/scan"
	"mediad/internal/timeline"
)

// fakeProcessor returns canned data or an error per segment id.
type fakeProcessor struct {
	data    map[int][]byte
	corrupt map[int]bool
	calls   []int
}

func (f *fakeProcessor) Process(seg Segment) ([]byte, error) {
	f.calls = append(f.calls, seg.ID)
	if f.corrupt[seg.ID] {
		return nil, ErrSourceCorrupt
	}
	if d, ok := f.data[seg.ID]; ok {
		return d, nil
	}
	return []byte("derived"), nil
}

func buildNormalized() *timeline.Normalized {
	base := model.Rational{Num: 1, Den: 1000}
	// 6 samples; sync samples at 0 and 3 => two segments.
	var samples []timeline.NormalizedSample
	for i := 0; i < 6; i++ {
		samples = append(samples, timeline.NormalizedSample{
			TrackID:    1,
			StartNanos: int64(i) * 1_000_000_000,
			EndNanos:   int64(i+1) * 1_000_000_000,
			Offset:     int64(i * 100),
			Length:     100,
			IsSync:     i == 0 || i == 3,
		})
	}
	return &timeline.Normalized{
		SourceHash: "src", SourcePath: "a.mp4",
		Tracks: []timeline.NormalizedTrack{{
			TrackSummary: scanTrack(1, base),
			Samples:      samples,
		}},
	}
}

func scanTrack(id uint32, base model.Rational) (ts scan.TrackSummary) {
	ts.ID = id
	ts.Kind = "video"
	ts.TimeBase = base
	return ts
}

func newSet(t *testing.T) *SegmentSet {
	t.Helper()
	blob := make([]byte, 600)
	for i := range blob {
		// Keep every byte unique across the whole blob so the two 300-byte
		// regions cannot alias to the same content digest.
		if i < 256 {
			blob[i] = byte(i)
		} else {
			blob[i] = byte(255 - (i % 256))
		}
	}
	n := buildNormalized()
	set, err := BuildSegments(n, 1, cache.Params{TargetKbps: 800, Pipeline: "v1"}, bytes.NewReader(blob))
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Segments) != 2 {
		t.Fatalf("segments = %d, want 2 (sync at sample 0 and 3)", len(set.Segments))
	}
	if set.Segments[0].CacheKey == set.Segments[1].CacheKey {
		t.Fatal("distinct segment content must produce distinct cache keys")
	}
	return set
}

func TestRecoveryOnlyRecomputesAffectedSegment(t *testing.T) {
	store, _ := cache.New(t.TempDir())
	proc := &fakeProcessor{data: map[int][]byte{0: []byte("seg0"), 1: []byte("seg1")}}
	r := New(store, proc)
	set := newSet(t)

	// First pass: no faults, both derived and cached.
	res1, err := r.Run(set, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res1.CacheHits) != 0 || len(proc.calls) != 2 {
		t.Fatalf("first pass should derive both: calls=%v hits=%v", proc.calls, res1.CacheHits)
	}

	// Second pass: no faults => both served from cache, processor unused.
	proc.calls = nil
	res2, _ := r.Run(set, nil)
	if len(res2.CacheHits) != 2 || len(proc.calls) != 0 {
		t.Fatalf("second pass must be all cache hits: calls=%v hits=%v", proc.calls, res2.CacheHits)
	}

	// Third pass: only segment 1 is faulty => recompute segment 1 alone.
	proc.calls = nil
	faults := map[int]model.Anomaly{
		1: {Type: model.AnomalyChecksum, Message: "bad checksum"},
	}
	res3, err := r.Run(set, faults)
	if err != nil {
		t.Fatal(err)
	}
	if len(res3.Recovered) != 1 || res3.Recovered[0] != 1 {
		t.Fatalf("only segment 1 should recover: %+v", res3.Recovered)
	}
	if len(proc.calls) != 1 || proc.calls[0] != 1 {
		t.Fatalf("processor must run once on segment 1 only: %v", proc.calls)
	}
	if len(res3.CacheHits) != 1 || res3.CacheHits[0] != 0 {
		t.Fatalf("healthy segment 0 must remain a cache hit: %v", res3.CacheHits)
	}
	anom := res3.Anomalies[0]
	if anom.Recovery != model.StateRecovered || anom.SourceOffset != 300 || anom.Checksum == "" {
		t.Fatalf("recovered anomaly wrong: %+v", anom)
	}
}

func TestRecoverySourceCorruptIsUnrecoverable(t *testing.T) {
	store, _ := cache.New(t.TempDir())
	proc := &fakeProcessor{corrupt: map[int]bool{1: true}}
	r := New(store, proc)
	set := newSet(t)
	// Segment 0 derives fine; segment 1's source is corrupt.
	res, err := r.Run(set, map[int]model.Anomaly{1: {Type: model.AnomalyUnrecoverable}})
	if err != nil {
		t.Fatalf("unrecoverable range must not hard-fail: %v", err)
	}
	if len(res.Unrecoverable) != 1 || res.Unrecoverable[0] != 1 {
		t.Fatalf("expected segment 1 unrecoverable: %v", res.Unrecoverable)
	}
	if len(res.CacheHits) != 0 {
		// segment 0 was derived this pass (not pre-cached), so no hit expected
	}
	for _, a := range res.Anomalies {
		if a.Recovery != model.StateUnrecoverable {
			t.Fatalf("corrupt range must stay unrecoverable: %+v", a)
		}
		if a.SourceOffset != 300 {
			t.Fatalf("unrecoverable range lost source offset: %+v", a)
		}
	}
}

func TestRecoveryPreservesHealthyResults(t *testing.T) {
	store, _ := cache.New(t.TempDir())
	proc := &fakeProcessor{corrupt: map[int]bool{1: true}}
	r := New(store, proc)
	set := newSet(t)
	res, _ := r.Run(set, map[int]model.Anomaly{1: {Type: model.AnomalyUnrecoverable}})
	// Healthy segment 0 must still be cached and retrievable despite segment 1.
	if !store.Has(set.Segments[0].CacheKey) {
		t.Fatal("healthy segment cache entry must survive a corrupt neighbour")
	}
	if len(res.Anomalies) != 1 {
		t.Fatalf("exactly one anomaly expected, got %d", len(res.Anomalies))
	}
}

func TestRecoveryNilSet(t *testing.T) {
	store, _ := cache.New(t.TempDir())
	r := New(store, &fakeProcessor{})
	if _, err := r.Run(nil, nil); err == nil {
		t.Fatal("nil segment set must error")
	}
}

func TestRecoveryStableOrdering(t *testing.T) {
	store, _ := cache.New(t.TempDir())
	proc := &fakeProcessor{}
	r := New(store, proc)
	set := newSet(t)
	faults := map[int]model.Anomaly{
		1: {Type: model.AnomalyChecksum},
		0: {Type: model.AnomalyGap},
	}
	res, _ := r.Run(set, faults)
	if len(res.Anomalies) != 2 {
		t.Fatalf("want 2 anomalies, got %d", len(res.Anomalies))
	}
	if res.Anomalies[0].StableKey() > res.Anomalies[1].StableKey() {
		t.Fatal("anomalies must be stably sorted")
	}
}
