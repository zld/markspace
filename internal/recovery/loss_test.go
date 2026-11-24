package recovery

import (
	"reflect"
	"testing"

	"mediasync/internal/model"
)

func ms(v int64) model.Nanos { return model.Nanos(v) * model.Nanos(model.NanosPerMilli) }

func pkt(ext int64, tm model.Nanos, kf bool) Packet {
	return Packet{Ext: ext, TrackID: 1, Kind: model.KindVideo, Keyframe: kf, MediaTime: tm, End: tm + ms(20)}
}

func TestContiguousVideoGap(t *testing.T) {
	in := Input{
		TrackID: 1, Kind: model.KindVideo,
		Packets: []Packet{
			pkt(1, ms(0), true), pkt(2, ms(20), false), pkt(5, ms(80), false),
		},
		KeyframeTimes: []model.Nanos{ms(0), ms(100)},
	}
	rep := Classify(in)
	if len(rep.Gaps) != 1 {
		t.Fatalf("gaps = %d, want 1", len(rep.Gaps))
	}
	g := rep.Gaps[0]
	if g.Count != 2 {
		t.Errorf("count = %d, want 2", g.Count)
	}
	if g.Kind != VideoKeyframeDependency {
		t.Errorf("kind = %s", g.Kind)
	}
	if g.Recoverable {
		t.Error("gap without redundancy must not be recoverable")
	}
	// The affected interval runs from the last good sample to the next
	// keyframe, the point where decoding can resume.
	if g.Start != ms(40) || g.End != ms(100) {
		t.Errorf("interval = [%d,%d), want [40ms,100ms)", g.Start, g.End)
	}
	if rep.Lost != 2 || rep.Unrecoverable != 2 {
		t.Errorf("lost=%d unrecoverable=%d", rep.Lost, rep.Unrecoverable)
	}
}

func TestAudioGapUsesCadence(t *testing.T) {
	in := Input{
		TrackID: 2, Kind: model.KindAudio, AudioCadence: ms(20),
		Packets: []Packet{
			{Ext: 1, TrackID: 2, Kind: model.KindAudio, MediaTime: ms(0), End: ms(20)},
			{Ext: 2, TrackID: 2, Kind: model.KindAudio, MediaTime: ms(20), End: ms(40)},
			{Ext: 6, TrackID: 2, Kind: model.KindAudio, MediaTime: ms(100), End: ms(120)},
		},
	}
	rep := Classify(in)
	if len(rep.Gaps) != 1 {
		t.Fatalf("gaps = %d", len(rep.Gaps))
	}
	g := rep.Gaps[0]
	if g.Kind != AudioInterval {
		t.Errorf("kind = %s, want audio interval", g.Kind)
	}
	if g.Count != 3 {
		t.Errorf("count = %d, want 3", g.Count)
	}
	// The estimate stops at the cadence-bounded end, before the next packet.
	if got, want := g.End, g.Start+ms(60); got != want {
		t.Errorf("end = %d, want %d", got, want)
	}
}

func TestRedundantGapRecoverable(t *testing.T) {
	in := Input{
		TrackID: 1, Kind: model.KindVideo,
		Packets: []Packet{
			pkt(1, ms(0), true), pkt(4, ms(60), false),
		},
		Repairs: []Repair{{Source: 99, Targets: []int64{2, 3}}},
	}
	rep := Classify(in)
	if len(rep.Gaps) != 1 {
		t.Fatalf("gaps = %d", len(rep.Gaps))
	}
	g := rep.Gaps[0]
	if !g.Recoverable {
		t.Error("fully repaired gap must be recoverable")
	}
	if g.Redundant != 2 || g.Count != 2 {
		t.Errorf("redundant=%d count=%d", g.Redundant, g.Count)
	}
	if rep.Unrecoverable != 0 {
		t.Errorf("unrecoverable = %d, want 0", rep.Unrecoverable)
	}
	if len(g.SourceSeq) != 2 || g.SourceSeq[0] != 99 {
		t.Errorf("source sequences = %v", g.SourceSeq)
	}
}

func TestPartiallyRedundantGap(t *testing.T) {
	in := Input{
		TrackID: 1, Kind: model.KindVideo,
		Packets: []Packet{pkt(1, ms(0), true), pkt(5, ms(80), false)},
		Repairs: []Repair{{Source: 50, Targets: []int64{2}}},
	}
	rep := Classify(in)
	if len(rep.Gaps) != 1 {
		t.Fatalf("gaps = %d", len(rep.Gaps))
	}
	g := rep.Gaps[0]
	if g.Recoverable {
		t.Error("partial repair must not be reported as recoverable")
	}
	if g.Redundant != 1 || g.Count != 3 {
		t.Errorf("redundant=%d count=%d", g.Redundant, g.Count)
	}
	if rep.Unrecoverable != 2 {
		t.Errorf("unrecoverable = %d, want 2", rep.Unrecoverable)
	}
}

func TestTrailingGapFromRepairs(t *testing.T) {
	in := Input{
		TrackID: 2, Kind: model.KindAudio, AudioCadence: ms(20),
		Packets: []Packet{
			{Ext: 1, TrackID: 2, Kind: model.KindAudio, MediaTime: ms(0), End: ms(20)},
			{Ext: 2, TrackID: 2, Kind: model.KindAudio, MediaTime: ms(20), End: ms(40)},
		},
		Repairs: []Repair{{Source: 60, Targets: []int64{3, 4}}},
	}
	rep := Classify(in)
	if len(rep.Gaps) != 1 {
		t.Fatalf("gaps = %d, want 1 trailing gap", len(rep.Gaps))
	}
	g := rep.Gaps[0]
	if g.Before != -1 {
		t.Errorf("trailing gap Before = %d, want -1", g.Before)
	}
	if g.Count != 2 {
		t.Errorf("count = %d, want 2", g.Count)
	}
	if g.Recoverable {
		t.Error("a hole with no following packet has no known recovery point")
	}
	if g.Start != ms(40) {
		t.Errorf("start = %d, want 40ms", g.Start)
	}
}

func TestNoGapsAndNoRepairs(t *testing.T) {
	in := Input{TrackID: 1, Kind: model.KindVideo, Packets: []Packet{pkt(1, ms(0), true), pkt(2, ms(20), false)}}
	rep := Classify(in)
	if len(rep.Gaps) != 0 || rep.Lost != 0 {
		t.Errorf("unexpected gaps: %+v", rep)
	}
	if got := rep.Intervals(); len(got) != 0 {
		t.Errorf("intervals = %v", got)
	}
}

func TestIntervalsMerged(t *testing.T) {
	r := Report{Gaps: []Gap{
		{Start: ms(0), End: ms(20)},
		{Start: ms(10), End: ms(40)},
		{Start: ms(60), End: ms(80)},
	}}
	got := r.Intervals()
	if len(got) != 2 {
		t.Fatalf("intervals = %v", got)
	}
	if got[0].Start != 0 || got[0].End != ms(40) {
		t.Errorf("first interval = %v", got[0])
	}
	if got[1].Start != ms(60) {
		t.Errorf("second interval = %v", got[1])
	}
}

func TestDeterministicOrdering(t *testing.T) {
	mk := func() Report {
		return Classify(Input{
			TrackID: 1, Kind: model.KindVideo,
			Packets: []Packet{pkt(5, ms(80), false), pkt(1, ms(0), true), pkt(3, ms(40), false)},
		})
	}
	a, b := mk(), mk()
	if len(a.Gaps) != len(b.Gaps) {
		t.Fatalf("gap counts differ")
	}
	for i := range a.Gaps {
		if !reflect.DeepEqual(a.Gaps[i], b.Gaps[i]) {
			t.Errorf("gap %d differs: %+v vs %+v", i, a.Gaps[i], b.Gaps[i])
		}
	}
}
