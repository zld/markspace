package merge

import (
	"testing"
)

func ev(ts int64, track, idx uint64) Event {
	return Event{TimestampNs: ts, Track: track, Index: idx, Kind: "audio"}
}

func TestMergeDeterministic(t *testing.T) {
	a := []Event{ev(0, 1, 0), ev(40, 1, 1), ev(80, 1, 2)}
	b := []Event{ev(5, 2, 0), ev(40, 2, 1), ev(85, 2, 2)}
	got := Merge(a, b)
	want := []Event{
		ev(0, 1, 0), ev(5, 2, 0), ev(40, 1, 1), ev(40, 2, 1), ev(80, 1, 2), ev(85, 2, 2),
	}
	if len(got) != len(want) {
		t.Fatalf("merged %d events, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// Same timestamp: track number breaks the tie (track 1 before 2).
	if got[2].Track != 1 || got[3].Track != 2 {
		t.Fatalf("tie order: %+v %+v", got[2], got[3])
	}
	// Merging the streams in reverse yields the same order.
	got2 := Merge(b, a)
	for i := range want {
		if got2[i] != want[i] {
			t.Fatalf("reversed merge event %d = %+v, want %+v", i, got2[i], want[i])
		}
	}
}

func TestSortStable(t *testing.T) {
	events := []Event{ev(10, 2, 0), ev(0, 1, 0), ev(10, 1, 1), ev(10, 1, 0)}
	Sort(events)
	for i := 1; i < len(events); i++ {
		if Less(events[i], events[i-1]) {
			t.Fatalf("not sorted at %d", i)
		}
	}
	if events[1].Track != 1 || events[1].Index != 0 || events[2].Index != 1 {
		t.Fatalf("tie order: %+v", events)
	}
}

func TestDigestResumable(t *testing.T) {
	events := []Event{ev(0, 1, 0), ev(5, 2, 0), ev(40, 1, 1)}
	// One-shot digest.
	h := DigestSeed()
	for _, e := range events {
		h = DigestEvent(h, e)
	}
	// Resumed digest: fold the first event, "checkpoint", continue.
	h2 := DigestSeed()
	h2 = DigestEvent(h2, events[0])
	for _, e := range events[1:] {
		h2 = DigestEvent(h2, e)
	}
	if h != h2 {
		t.Fatalf("resumed digest %x != one-shot %x", h2, h)
	}
	// Different sequences digest differently.
	h3 := DigestSeed()
	h3 = DigestEvent(h3, ev(0, 1, 0))
	h3 = DigestEvent(h3, ev(6, 2, 0))
	if h3 == DigestEvent(DigestEvent(DigestSeed(), events[0]), events[1]) {
		t.Fatal("digest insensitive to event content")
	}
}
