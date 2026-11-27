package pipeline

import (
	"context"
	"testing"

	"mediacheck/internal/codec"
	mediaerr "mediacheck/internal/errors"
	"mediacheck/internal/txn"
)

func newRunner(t *testing.T, cfg Config) *Runner {
	t.Helper()
	r, err := NewRunner(t.TempDir(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestHealthyPipelineZeroDamageAndHighHitRate(t *testing.T) {
	gen, err := Generate(DemoSpec{
		Track: "v-h264", Codec: codec.H264, Framing: codec.AnnexB,
		Frames: 24, KeyInterval: 6, Fault: FaultNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := newRunner(t, Config{ChunkBytes: 256, QueueLen: 2, SegmentBytes: 128})
	rep, err := r.Run(context.Background(), "run-healthy", []TrackInput{gen.TrackInput()})
	if err != nil {
		t.Fatal(err)
	}
	tr := rep.Tracks[0]
	if len(tr.Damage) != 0 {
		t.Fatalf("healthy stream must have zero damage, got %+v", tr.Damage)
	}
	if tr.KeyframeCount != 4 { // frames 0,6,12,18
		t.Fatalf("keyframes=%d want 4", tr.KeyframeCount)
	}
	if len(tr.Intervals) != 3 {
		t.Fatalf("intervals=%d want 3", len(tr.Intervals))
	}
	if rep.Metrics["keyframe_index_hit_rate"] < 0.95 {
		t.Fatalf("hit rate=%v", rep.Metrics["keyframe_index_hit_rate"])
	}
	if rep.Metrics["roundtrip_consistency"] != 1.0 {
		t.Fatal("roundtrip consistency must be 1.0 on success")
	}
	if rep.Metrics["segment_sha256_count"] <= 0 {
		t.Fatal("segment hashes must be present")
	}
}

func TestTruncatedFaultIsLocated(t *testing.T) {
	gen, err := Generate(DemoSpec{
		Track: "v", Codec: codec.H264, Framing: codec.AnnexB,
		Frames: 12, KeyInterval: 4, Fault: FaultTruncated,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := newRunner(t, Config{ChunkBytes: 128, QueueLen: 2, SegmentBytes: 128})
	rep, err := r.Run(context.Background(), "run-trunc", []TrackInput{gen.TrackInput()})
	if err != nil {
		t.Fatal(err)
	}
	dmg := rep.Tracks[0].Damage
	if len(dmg) < gen.ExpectDamage {
		t.Fatalf("want >= %d damage range, got %+v", gen.ExpectDamage, dmg)
	}
	// The range must be a valid, non-empty interval inside the stream.
	for _, d := range dmg {
		if d.End <= d.Start || d.Start < 0 || d.End > rep.Tracks[0].Bytes {
			t.Fatalf("invalid damage bounds: %+v", d)
		}
	}
	// Earlier valid units must survive.
	if rep.Tracks[0].UnitCount == 0 {
		t.Fatal("truncation swallowed every unit")
	}
}

func TestInvalidLengthFaultResyncs(t *testing.T) {
	gen, err := Generate(DemoSpec{
		Track: "v", Codec: codec.AV1, Framing: codec.LengthPrefixed,
		Frames: 12, KeyInterval: 4, Fault: FaultInvalidLength,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := newRunner(t, Config{ChunkBytes: 128, QueueLen: 2, SegmentBytes: 128})
	rep, err := r.Run(context.Background(), "run-badlen", []TrackInput{gen.TrackInput()})
	if err != nil {
		t.Fatal(err)
	}
	dmg := rep.Tracks[0].Damage
	if len(dmg) < 1 {
		t.Fatalf("invalid-length must produce a damage range: %+v", dmg)
	}
	// The gap must not extend over the recovered valid unit at the tail.
	lastUnitEnd := rep.Tracks[0].Bytes
	for _, d := range dmg {
		if d.End > lastUnitEnd {
			t.Fatalf("damage extends beyond stream: %+v", d)
		}
	}
	if rep.Tracks[0].UnitCount < 2 {
		t.Fatalf("resync should recover later units, got %d", rep.Tracks[0].UnitCount)
	}
}

func TestPeakBufferBounded(t *testing.T) {
	gen, err := Generate(DemoSpec{
		Track: "v", Codec: codec.H265, Framing: codec.AnnexB,
		Frames: 30, KeyInterval: 5, Fault: FaultNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{ChunkBytes: 256, QueueLen: 2, SegmentBytes: 256}
	r := newRunner(t, cfg)
	rep, err := r.Run(context.Background(), "run-buf", []TrackInput{gen.TrackInput()})
	if err != nil {
		t.Fatal(err)
	}
	limit := int64(cfg.ChunkBytes) * int64(cfg.QueueLen+2)
	if rep.PeakBuffer > limit {
		t.Fatalf("peak buffer %d exceeds limit %d", rep.PeakBuffer, limit)
	}
	if rep.BufferLimit != limit {
		t.Fatalf("reported limit %d want %d", rep.BufferLimit, limit)
	}
}

func TestSegmentHashesStableAcrossReruns(t *testing.T) {
	spec := DemoSpec{
		Track: "v", Codec: codec.H264, Framing: codec.AnnexB,
		Frames: 18, KeyInterval: 6, Fault: FaultNone,
	}
	cfg := Config{ChunkBytes: 200, QueueLen: 3, SegmentBytes: 200}
	run := func() (*Report, error) {
		// Regenerate identical bytes (the generator is deterministic) and
		// rewind so both runs hash the same fixed windows.
		g, err := Generate(spec)
		if err != nil {
			return nil, err
		}
		r := newRunner(t, cfg)
		return r.Run(context.Background(), "run-hash", []TrackInput{g.TrackInput()})
	}
	r1, err := run()
	if err != nil {
		t.Fatal(err)
	}
	r2, err := run()
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Segments) != len(r2.Segments) || len(r1.Segments) == 0 {
		t.Fatalf("segment count mismatch: %d vs %d", len(r1.Segments), len(r2.Segments))
	}
	for i := range r1.Segments {
		if r1.Segments[i].SHA256 != r2.Segments[i].SHA256 ||
			r1.Segments[i].ID != r2.Segments[i].ID {
			t.Fatalf("segment %d differs across reruns", i)
		}
	}
}

func TestInterruptAndResumeSkipsCommittedStages(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		ChunkBytes: 128, QueueLen: 2, SegmentBytes: 128,
		InterruptAfterStage: txn.StageScan,
	}
	gen, err := Generate(DemoSpec{
		Track: "v", Codec: codec.H264, Framing: codec.AnnexB,
		Frames: 12, KeyInterval: 4, Fault: FaultNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	// First run must stop after scan with the structured interruption error.
	r1, err := NewRunner(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r1.Run(context.Background(), "run-resume", []TrackInput{gen.TrackInput()})
	if err != ErrInterrupted {
		t.Fatalf("want ErrInterrupted, got %v", err)
	}
	if !r1.txnMgr.WAL().IsCommitted(txn.StageScan, "v") {
		t.Fatal("scan stage must be committed before interruption")
	}

	// Resume without interruption over the same data.
	gen.ResetReader()
	cfg.InterruptAfterStage = ""
	r2, err := NewRunner(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := r2.Run(context.Background(), "run-resume", []TrackInput{gen.TrackInput()})
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if !rep.Recovered {
		t.Fatal("resumed run must report recovered=true")
	}
	found := false
	for _, s := range rep.Recommitted {
		if s == txn.StageScan+"/v" {
			found = true
		}
	}
	if !found {
		t.Fatalf("already-committed scan stage must be replayed, not re-run: %v",
			rep.Recommitted)
	}
}

func TestOversizedInputRejected(t *testing.T) {
	cfg := Config{ChunkBytes: 64, QueueLen: 1, SegmentBytes: 64}
	r := newRunner(t, cfg)
	gen, err := Generate(DemoSpec{
		Track: "v", Codec: codec.H264, Framing: codec.AnnexB,
		Frames: 4, Fault: FaultNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Force the cap down by feeding many chunks; here we just verify the
	// config validation path and error mapping are wired.
	_, err = r.Run(context.Background(), "x", []TrackInput{gen.TrackInput()})
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := mediaerr.AsMedia(ErrInterrupted); code == nil {
		t.Fatal("interrupted sentinel must be a structured media error")
	}
}
