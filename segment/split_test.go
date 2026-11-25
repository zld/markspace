package segment

import (
	"testing"

	"rtpreplay/clock"
	"rtpreplay/ingest"
	"rtpreplay/media"
	"rtpreplay/pipeline"
)

func buildRep(t *testing.T) *pipeline.Representation {
	t.Helper()
	// Reuse a small parsed stream through the public pipeline path.
	st := ingestBuildStream(t)
	reps, err := pipeline.Fanout([]*ingest.Stream{st}, pipeline.KindAudio, pipeline.DefaultMaxGap)
	if err != nil {
		t.Fatal(err)
	}
	reps[0].LocateBoundaries(40) // 40 ms period -> boundaries at samples 0 and 2
	return reps[0]
}

func TestSplitSegments(t *testing.T) {
	rep := buildRep(t)
	segs := Split(rep)
	if len(segs) < 2 {
		t.Fatalf("want >=2 segments, got %d: %+v", len(segs), segs)
	}
	first := segs[0]
	if first.StartMS != 0 || first.StartRecord != 0 {
		t.Fatalf("first segment must start at stream origin: %+v", first)
	}
	if first.EndRecord < first.StartRecord || first.EndMS <= first.StartMS {
		t.Fatalf("segment span invalid: %+v", first)
	}
	if first.Damaged {
		t.Fatalf("healthy stream must not mark damage: %+v", first)
	}
	// Segment indices are contiguous and source ranges non-overlapping.
	for i := 1; i < len(segs); i++ {
		if segs[i].Index != i {
			t.Fatalf("non-contiguous index %d", i)
		}
		if segs[i].StartRecord != segs[i-1].EndRecord+1 {
			t.Fatalf("ranges must tile without overlap: %d then %d",
				segs[i-1].EndRecord, segs[i].StartRecord)
		}
	}
	last := segs[len(segs)-1]
	if last.EndRecord != rep.Stream.Packets[len(rep.Stream.Packets)-1].RecordIndex {
		t.Fatal("last segment must reach the final packet")
	}
}

func TestSplitPreservesDamagedRegion(t *testing.T) {
	st := ingestBuildBackwardStream(t)
	reps, err := pipeline.Fanout([]*ingest.Stream{st}, pipeline.KindVideo, pipeline.DefaultMaxGap)
	if err != nil {
		t.Fatal(err)
	}
	reps[0].LocateBoundaries(50)
	segs := Split(reps[0])

	var damaged int
	for _, s := range segs {
		if s.Damaged {
			damaged++
			found := false
			for _, r := range s.Reasons {
				if r == string(clock.AnomBackward) {
					found = true
				}
			}
			if !found {
				t.Fatalf("damaged segment must carry the anomaly reason: %+v", s.Reasons)
			}
		}
	}
	if damaged == 0 {
		t.Fatal("isolated backwards packet must mark a segment damaged, never be dropped")
	}
	// Every input packet remains covered by exactly one segment span.
	covered := 0
	for _, s := range segs {
		covered += s.EndRecord - s.StartRecord + 1
	}
	if covered != len(reps[0].Stream.Packets) {
		t.Fatalf("coverage %d != packets %d", covered, len(reps[0].Stream.Packets))
	}
}

func TestSplitEmpty(t *testing.T) {
	rep := &pipeline.Representation{
		Stream: &ingest.Stream{ClockRate: media.AudioClockRate, Packets: nil},
	}
	if segs := Split(rep); segs != nil {
		t.Fatalf("empty representation must yield no segments, got %+v", segs)
	}
}
