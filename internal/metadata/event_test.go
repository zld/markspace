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
	var same, different int
	for _, e := range got.Events {
		switch string(e.Data) {
		case "same":
			same++
		case "different":
			different++
		}
	}
	if same != 3 || different != 1 {
		t.Fatalf("content-different events must be kept: %+v", got.Events)
	}
	if got.Events[3].Time.Ticks != 200 {
		t.Fatalf("later time must sort last: %+v", got.Events)
	}
}

func TestMergeIsStableForEqualKeys(t *testing.T) {
	// Events sharing track, kind and time are ordered by content digest, so the
	// result does not depend on the order in which the demuxer emitted them.
	a := ev(KindID3, 1, clock.New(1, 1000), "a", 10)
	b := ev(KindID3, 1, clock.New(1, 1000), "b", 11)
	forward, err := Merge([]Event{a, b})
	if err != nil {
		t.Fatal(err)
	}
	backward, err := Merge([]Event{b, a})
	if err != nil {
		t.Fatal(err)
	}
	if len(forward.Events) != 2 || len(backward.Events) != 2 {
		t.Fatalf("unexpected counts: %d %d", len(forward.Events), len(backward.Events))
	}
	for i := range forward.Events {
		if forward.Events[i].Digest() != backward.Events[i].Digest() {
			t.Fatalf("order depends on input order: %+v vs %+v", forward.Events, backward.Events)
		}
	}
}
