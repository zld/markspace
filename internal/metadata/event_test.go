package metadata

import (
	"testing"

	"mediapipeline/internal/clock"
)

func ev(kind Kind, track uint32, t clock.Time, data string, seq int) Event {
	return Event{Kind: kind, TrackID: track, Time: t, Data: []byte(data), Seq: seq}
}

func TestMergeSortsAcrossTimescales(t *testing.T) {
	events := []Event{
		ev(KindSubtitle, 3, clock.New(1500, 1000), "late", 3),
		ev(KindSEI, 1, clock.New(90000, 90000), "sei", 2),
		ev(KindChapter, 4, clock.New(500, 1000), "chap", 1),
	}
	got, err := Merge(events)
	if err != nil {
		t.Fatal(err)
	}
	if got.Timescale != 90000 {
		t.Fatalf("timescale: %d", got.Timescale)
	}
	if len(got.Events) != 3 || got.Events[0].Data[0] != 'c' || got.Events[1].Data[0] != 's' || got.Events[2].Data[0] != 'l' {
		t.Fatalf("bad order: %+v", got.Events)
	}
	for _, e := range got.Events {
		if e.Time.Timescale != 90000 {
			t.Fatalf("event not rescaled: %v", e.Time)
		}
	}
}

func TestMergeDeduplicatesOnlyIdenticalContent(t *testing.T) {
	events := []Event{
		ev(KindEmsg, 2, clock.New(100, 1000), "same", 1),
		ev(KindEmsg, 2, clock.New(100, 1000), "same", 2),
		ev(KindEmsg, 2, clock.New(100, 1000), "different", 3),
		ev(KindEmsg, 3, clock.New(100, 1000), "same", 4),
		ev(KindEmsg, 2, clock.New(200, 1000), "same", 5),
	}
	got, err := Merge(events)
	if err != nil {
		t.Fatal(err)
	}
	if got.DuplicateCount != 1 || len(got.Events) != 4 {
		t.Fatalf("dups=%d out=%d", got.DuplicateCount, len(got.Events))
	}
	if got.Events[1].Data[0] != 'd' || got.Events[2].Data[0] != 's' || got.Events[3].Data[0] != 's' {
		t.Fatalf("content-different events must be kept: %+v", got.Events)
	}
	if got.Events[2].TrackID != 3 || got.Events[3].Time.Ticks != 200 {
		t.Fatalf("content-different events must be kept: %+v", got.Events)
	}
}

func TestMergeIsStableForEqualKeys(t *testing.T) {
	events := []Event{
		ev(KindID3, 1, clock.New(1, 1000), "a", 10),
		ev(KindID3, 1, clock.New(1, 1000), "b", 11),
	}
	for i := 0; i < 5; i++ {
		got, err := Merge(events)
		if err != nil {
			t.Fatal(err)
		}
		if got.Events[0].Seq != 10 || got.Events[1].Seq != 11 {
			t.Fatalf("unstable: %+v", got.Events)
		}
	}
}
