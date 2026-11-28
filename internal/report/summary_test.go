package report

import (
	"testing"

	"medcheck/internal/index"
	"medcheck/internal/model"
	"medcheck/internal/repair"
	"medcheck/internal/timeline"
)

func mkFrame(track, source, seq uint32, micros int64, start int64, valid bool, ftype model.FrameType) *model.Frame {
	o := model.RawOffset{Stream: "s", Start: start, End: start + 4}
	f := &model.Frame{
		Track:      track,
		Source:     source,
		Seq:        seq,
		EventTime:  model.Timestamp{Micros: micros, HasMicros: true, Offset: o, Base: model.Rational{Num: 1e6, Den: 1}},
		Type:       ftype,
		Offset:     o,
		Valid:      valid,
		PayloadLen: 4,
	}
	if !valid {
		f.Err = model.ChecksumError(o, track, source, seq)
	}
	return f
}

func run(frames []*model.Frame, window int64) *Summary {
	view := timeline.Replay(frames, 1_000_000)
	idx := index.Build(view.ValidFrames())
	segs := repair.Build(view.Ordered)
	return Build(Inputs{View: view, Index: idx, Segments: segs, WindowMicros: window})
}

func TestShardsUseFixedTrackTimeBoundary(t *testing.T) {
	frames := []*model.Frame{
		mkFrame(1, 1, 0, 0, 0, true, model.TypeKeyFrame),
		mkFrame(1, 1, 1, 50, 4, true, model.TypeDelta),
		mkFrame(1, 1, 2, 120, 8, true, model.TypeDelta),
	}
	shards := BuildShards(frames, 100)
	// buckets: 0,0,100 -> two shards.
	if len(shards) != 2 {
		t.Fatalf("shards = %d want 2", len(shards))
	}
	if shards[0].Bucket != 0 || shards[0].FrameCount != 2 {
		t.Fatalf("shard0 = %+v", shards[0])
	}
	if shards[1].Bucket != 100 || shards[1].FrameCount != 1 {
		t.Fatalf("shard1 = %+v", shards[1])
	}
}

func TestShardHashStableAcrossArrivalOrder(t *testing.T) {
	a := []*model.Frame{
		mkFrame(1, 1, 0, 0, 0, true, model.TypeKeyFrame),
		mkFrame(1, 1, 1, 50, 4, true, model.TypeDelta),
		mkFrame(1, 1, 2, 120, 8, true, model.TypeDelta),
	}
	b := []*model.Frame{a[2], a[0], a[1]}
	ha := BuildShards(a, 100)
	hb := BuildShards(b, 100)
	if len(ha) != len(hb) {
		t.Fatalf("shard count differs: %d vs %d", len(ha), len(hb))
	}
	for i := range ha {
		if ha[i].SHA256 != hb[i].SHA256 || ha[i].Bucket != hb[i].Bucket {
			t.Fatalf("shard %d hash differs", i)
		}
	}
}

func TestSummaryHashReplayEqual(t *testing.T) {
	a := []*model.Frame{
		mkFrame(1, 1, 0, 0, 0, true, model.TypeKeyFrame),
		mkFrame(1, 1, 1, 10, 4, false, model.TypeDelta),
		mkFrame(1, 1, 2, 20, 8, true, model.TypeKeyFrame),
	}
	b := []*model.Frame{a[2], a[1], a[0]}
	s1 := run(a, 100)
	s2 := run(b, 100)
	if s1.Hash() != s2.Hash() {
		t.Fatalf("summary hash differs across arrival order:\n%s\n%s", s1.Hash(), s2.Hash())
	}
	if s1.BadSegments != 1 || s1.KeyframeCount != 2 {
		t.Fatalf("counts wrong: bad=%d key=%d", s1.BadSegments, s1.KeyframeCount)
	}
}

func TestKeyframeHitRate(t *testing.T) {
	s := &Summary{}
	s.RecordKeyframeQuery(true)
	s.RecordKeyframeQuery(true)
	s.RecordKeyframeQuery(false)
	if s.KeyframeHitRate() != 2.0/3.0 {
		t.Fatalf("hit rate = %f", s.KeyframeHitRate())
	}
	empty := &Summary{}
	if empty.KeyframeHitRate() != 1.0 {
		t.Fatal("no queries should report a perfect rate")
	}
}

func TestSegmentHashCountExposed(t *testing.T) {
	frames := []*model.Frame{
		mkFrame(1, 1, 0, 0, 0, false, model.TypeKeyFrame),
		mkFrame(1, 1, 1, 10, 4, true, model.TypeDelta),
		mkFrame(1, 1, 2, 20, 8, false, model.TypeKeyFrame),
	}
	s := run(frames, 100)
	if s.BadSegments != 2 || len(s.SegmentHashes) != 2 {
		t.Fatalf("bad segments = %d hashes = %d", s.BadSegments, len(s.SegmentHashes))
	}
}
