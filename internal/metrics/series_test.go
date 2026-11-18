package metrics

import (
	"errors"
	"testing"

	"jitterlab/internal/buffer"
	"jitterlab/internal/conceal"
	"jitterlab/internal/packet"
)

func TestRecorderCountsDecodeFailuresSeparately(t *testing.T) {
	r := New(buffer.Config{FrameMs: 20}, 1)
	r.ObserveDatagram(32, nil)
	r.ObserveDatagram(10, packet.ErrTruncated)
	r.ObserveDatagram(32, packet.ErrUnknownVersion)
	r.ObserveDatagram(32, errors.Join(errors.New("wrap"), packet.ErrChecksum))
	r.ObserveDatagram(32, packet.ErrChecksum)
	r.ObserveDatagram(32, packet.ErrBadLength)

	p := r.Protocol()
	if p.Input != 6 || p.Valid != 1 || p.Invalid != 5 {
		t.Fatalf("counters = %+v", p)
	}
	if p.Truncated != 1 || p.UnknownVersion != 1 || p.Checksum != 2 || p.BadLength != 1 {
		t.Fatalf("reason counters = %+v", p)
	}
	if p.Consecutive != 5 {
		t.Fatalf("longest bad run = %d, want 5", p.Consecutive)
	}

	s := r.Summary(buffer.Stats{Duplicates: 2, Reordered: 3, Dropped: 4})
	if s.DuplicatePackets != 2 || s.ReorderedPackets != 3 || s.DroppedPackets != 4 {
		t.Fatalf("summary passthrough = %+v", s)
	}
	if s.Truncated != 1 || s.VersionErrors != 1 || s.ChecksumErrors != 2 || s.LongestBadRun != 5 {
		t.Fatalf("summary protocol = %+v", s)
	}
}

func TestRecorderSlotAggregates(t *testing.T) {
	r := New(buffer.Config{FrameMs: 20, TargetMs: 80, MaxWaitMs: 200, WindowSize: 32}, 9)
	slots := []buffer.Slot{
		{Played: true, Seq: 0, Kind: conceal.KindReal, BufferMs: 80, TargetMs: 80},
		{Played: true, Seq: 1, Kind: conceal.KindExtend, BufferMs: 60, TargetMs: 80, ConcealRun: 1},
		{Played: true, Seq: 2, Kind: conceal.KindDecay, BufferMs: 40, TargetMs: 80, ConcealRun: 2},
		{Played: true, Seq: 3, Kind: conceal.KindSilence, BufferMs: 20, TargetMs: 80, ConcealRun: 3},
		{Played: true, Seq: 4, Kind: conceal.KindReal, BufferMs: 100, TargetMs: 80, Recovered: true, Delta: 12},
	}
	for i, slot := range slots {
		rec := r.ObserveSlot(slot)
		if rec.Frame != i+1 || rec.Seq != slot.Seq || rec.Kind != slot.Kind.String() {
			t.Fatalf("frame %d = %+v", i, rec)
		}
	}
	s := r.Summary(buffer.Stats{})
	if s.Frames != 5 || s.ConcealedFrames != 3 || s.SilentFrames != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if s.MaxConcealRun != 3 {
		t.Fatalf("max run = %d, want 3", s.MaxConcealRun)
	}
	if s.BufferMsMin != 20 || s.BufferMsMax != 100 {
		t.Fatalf("buffer range = %d..%d", s.BufferMsMin, s.BufferMsMax)
	}
	if s.BufferMsAvg != 60 {
		t.Fatalf("buffer avg = %v, want 60", s.BufferMsAvg)
	}
	if s.Recoveries != 1 || s.ContinuityDelta != 12 {
		t.Fatalf("continuity = %+v", s)
	}
	if s.ConcealRatio != 0.6 {
		t.Fatalf("conceal ratio = %v, want 0.6", s.ConcealRatio)
	}
	if s.MaxWaitMs != 200 || s.WindowSize != 32 || s.FrameMs != 20 {
		t.Fatalf("config echo = %+v", s)
	}
}
