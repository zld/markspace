package index

import (
	"testing"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/model"
)

func sampleTrack(t *testing.T, n int, step int64, syncEvery int) []model.Sample {
	t.Helper()
	out := make([]model.Sample, 0, n)
	for i := 0; i < n; i++ {
		r, err := clock.FromTicks(int64(i)*step, 90000)
		if err != nil {
			t.Fatal(err)
		}
		d, _ := clock.FromTicks(step, 90000)
		out = append(out, model.Sample{
			Index: i, Track: model.TrackVideoMain,
			Time:     model.TimeStamp{RawTicks: int64(i) * step, RepairedTime: clock.ToModel(r)},
			Duration: clock.ToModel(d), Size: 100, Sync: syncEvery > 0 && i%syncEvery == 0,
		})
	}
	return out
}

func TestBuildKeyframesReachesEverySyncSample(t *testing.T) {
	samples := sampleTrack(t, 90, 3000, 30)
	segments := []int64{0, 180000}
	offsets := make([]uint64, len(samples))
	for i := range offsets {
		offsets[i] = uint64(i) * 100
	}
	kf, err := BuildKeyframes(model.TrackVideoMain, 90000, samples, offsets, segments)
	if err != nil {
		t.Fatalf("BuildKeyframes: %v", err)
	}
	if len(kf.Entries) != 3 {
		t.Fatalf("keyframes = %d, want 3", len(kf.Entries))
	}
	if kf.Entries[0].Ticks != 0 || kf.Entries[1].Ticks != 90000 || kf.Entries[2].Ticks != 180000 {
		t.Fatalf("keyframe ticks = %+v", kf.Entries)
	}
	// Sample 60 is the keyframe at 2 s; its offset is 60*100 bytes.
	if kf.Entries[2].ByteOffset != 6000 {
		t.Fatalf("byte offset = %d, want 6000", kf.Entries[2].ByteOffset)
	}
	// Lookup at a mid-segment time returns the covering keyframe.
	e, ok := kf.Lookup(100000)
	if !ok || e.Ticks != 90000 {
		t.Fatalf("Lookup(100000) = %+v, %v", e, ok)
	}
	if _, ok := kf.Lookup(-1); ok {
		t.Fatal("a tick before the index must not resolve")
	}
}

func TestBuildKeyframesRejectsNonSyncCandidates(t *testing.T) {
	// Only sample 0 and 30 are sync; a sample that is not sync is skipped.
	samples := sampleTrack(t, 90, 3000, 30)
	kf, err := BuildKeyframes(model.TrackVideoMain, 90000, samples, nil, []int64{0, 180000})
	if err != nil {
		t.Fatal(err)
	}
	if len(kf.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(kf.Entries))
	}
	if kf.Rejected != 0 {
		t.Fatalf("rejected = %d, want 0", kf.Rejected)
	}
}

func TestBuildKeyframesErrors(t *testing.T) {
	if _, err := BuildKeyframes(model.TrackVideoMain, 0, sampleTrack(t, 5, 3000, 1), nil, []int64{0}); err == nil {
		t.Fatal("zero timescale should fail")
	}
	if _, err := BuildKeyframes(model.TrackVideoMain, 90000, nil, nil, []int64{0}); err == nil {
		t.Fatal("no samples should fail")
	}
	if _, err := BuildKeyframes(model.TrackVideoMain, 90000, sampleTrack(t, 5, 3000, 1), nil, nil); err == nil {
		t.Fatal("empty segment plan should fail")
	}
	if _, err := BuildKeyframes(model.TrackVideoMain, 90000, sampleTrack(t, 5, 3000, 1), []uint64{1}, []int64{0}); err == nil {
		t.Fatal("mismatched byte offsets should fail")
	}
}

func TestBuildKeyframesDeterministic(t *testing.T) {
	samples := sampleTrack(t, 60, 3000, 15)
	seg := []int64{0, 90000}
	a, err := BuildKeyframes(model.TrackVideoMain, 90000, samples, nil, seg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildKeyframes(model.TrackVideoMain, 90000, samples, nil, seg)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Entries) != len(b.Entries) {
		t.Fatal("entry count differs between runs")
	}
	for i := range a.Entries {
		if a.Entries[i] != b.Entries[i] {
			t.Fatalf("entry %d differs: %+v vs %+v", i, a.Entries[i], b.Entries[i])
		}
	}
}

func TestHitRate(t *testing.T) {
	samples := sampleTrack(t, 90, 3000, 30)
	kf, err := BuildKeyframes(model.TrackVideoMain, 90000, samples, nil, []int64{0, 180000})
	if err != nil {
		t.Fatal(err)
	}
	var want []int64
	for _, s := range samples {
		if s.Sync {
			r, _ := clock.FromModel(s.Time.RepairedTime)
			tk, _ := r.FloorTicks(90000)
			want = append(want, tk)
		}
	}
	if got := HitRate(kf, want); got != 1 {
		t.Fatalf("hit rate = %v, want 1", got)
	}
	if got := HitRate(kf, []int64{1}); got != 0 {
		t.Fatalf("hit rate for a non-keyframe = %v, want 0", got)
	}
	if got := HitRate(kf, nil); got != 1 {
		t.Fatalf("empty expectation = %v, want 1", got)
	}
}

func segData(payloads map[int][]byte) SampleData {
	return func(i int) ([]byte, bool) {
		p, ok := payloads[i]
		return p, ok
	}
}

func TestBuildSegmentsDeterministicHashes(t *testing.T) {
	samples := sampleTrack(t, 60, 3000, 30)
	payloads := map[int][]byte{}
	for i := range samples {
		payloads[i] = []byte{byte(i), byte(i >> 8), 0x5a}
	}
	segs := []Segment{{Index: 0, StartTick: 0, EndTick: 90000}, {Index: 1, StartTick: 90000, EndTick: 180000}}
	a, err := BuildSegments(model.TrackVideoMain, 90000, samples, segData(payloads), segs, nil)
	if err != nil {
		t.Fatalf("BuildSegments: %v", err)
	}
	b, err := BuildSegments(model.TrackVideoMain, 90000, samples, segData(payloads), segs, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range a {
		if a[i].Hash != b[i].Hash {
			t.Fatalf("segment %d hash is not reproducible", i)
		}
		if a[i].SampleCount == 0 {
			t.Fatalf("segment %d counted no samples", i)
		}
	}
	if a[0].Hash == a[1].Hash {
		t.Fatal("different segments must hash differently")
	}
}

func TestBuildSegmentsCountsMissingPayloads(t *testing.T) {
	samples := sampleTrack(t, 30, 3000, 30)
	payloads := map[int][]byte{}
	for i := 0; i < 30; i++ {
		if i == 5 {
			continue // missing payload
		}
		payloads[i] = []byte{byte(i)}
	}
	segs := []Segment{{Index: 0, StartTick: 0, EndTick: 90000}}
	idx, err := BuildSegments(model.TrackVideoMain, 90000, samples, segData(payloads), segs, nil)
	if err != nil {
		t.Fatalf("a missing payload must not fail the segment: %v", err)
	}
	if idx[0].Missing != 1 || !idx[0].Partial {
		t.Fatalf("segment = %+v, want partial with one missing payload", idx[0])
	}
}

func TestBuildSegmentsEventsAndVerify(t *testing.T) {
	samples := sampleTrack(t, 30, 3000, 30)
	payloads := map[int][]byte{}
	for i := range samples {
		payloads[i] = []byte{byte(i)}
	}
	segs := []Segment{{Index: 0, StartTick: 0, EndTick: 90000}}
	r, _ := clock.FromTicks(3000, 90000)
	events := []model.Event{
		{Kind: model.MetaSubtitle, ID: "cue-1", Time: model.TimeStamp{RepairedTime: clock.ToModel(r)}},
	}
	idx, err := BuildSegments(model.TrackVideoMain, 90000, samples, segData(payloads), segs, events)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx[0].EventIDs) != 1 || idx[0].EventIDs[0] != "subtitle:cue-1" {
		t.Fatalf("event ids = %v", idx[0].EventIDs)
	}
	matched, total, err := VerifySegments(model.TrackVideoMain, 90000, samples, segData(payloads), segs, idx, events)
	if err != nil {
		t.Fatal(err)
	}
	if matched != total || total != 1 {
		t.Fatalf("verify = %d/%d, want 1/1", matched, total)
	}
	// Tampering with a stored hash breaks verification.
	idx[0].Hash = "00"
	matched, _, err = VerifySegments(model.TrackVideoMain, 90000, samples, segData(payloads), segs, idx, events)
	if err != nil {
		t.Fatal(err)
	}
	if matched != 0 {
		t.Fatal("tampered hash must not verify")
	}
}

func TestBuildSegmentsRejectsBadPlan(t *testing.T) {
	samples := sampleTrack(t, 10, 3000, 10)
	if _, err := BuildSegments(model.TrackVideoMain, 90000, samples, nil, []Segment{{Index: 0, StartTick: 0, EndTick: 0}}, nil); err == nil {
		t.Fatal("zero-length segment should fail")
	}
	if _, err := BuildSegments(model.TrackVideoMain, 90000, samples, nil, nil, nil); err == nil {
		t.Fatal("empty plan should fail")
	}
	if _, err := BuildSegments(model.TrackVideoMain, 0, samples, nil, []Segment{{Index: 0, StartTick: 0, EndTick: 1000}}, nil); err == nil {
		t.Fatal("zero timescale should fail")
	}
}
