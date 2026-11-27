package syncer

import (
	"testing"

	"continuity/internal/merge"
)

func TestAlign(t *testing.T) {
	var events []merge.Event
	// Video every 40 ms, audio every 20 ms offset by +5 ms.
	for t := int64(0); t <= 160; t += 40 {
		events = append(events, merge.Event{TimestampNs: t * 1_000_000, Track: 1, Kind: "video"})
	}
	for t := int64(5); t <= 165; t += 20 {
		events = append(events, merge.Event{TimestampNs: t * 1_000_000, Track: 2, Kind: "audio"})
	}
	merge.Sort(events)
	s := Align(events)
	if s.Pairs != 5 {
		t.Fatalf("pairs = %d, want 5", s.Pairs)
	}
	if s.MaxDeviationNs != 5_000_000 {
		t.Fatalf("max deviation = %d, want 5000000", s.MaxDeviationNs)
	}
}

func TestAlignNearest(t *testing.T) {
	events := []merge.Event{
		{TimestampNs: 0, Track: 1, Kind: "video"},
		{TimestampNs: 100, Track: 1, Kind: "video"},
		{TimestampNs: 90, Track: 2, Kind: "audio"},
	}
	merge.Sort(events)
	s := Align(events)
	if s.Pairs != 2 {
		t.Fatalf("pairs = %d, want 2", s.Pairs)
	}
	// video@0 -> 90, video@100 -> 10.
	if s.MaxDeviationNs != 90 {
		t.Fatalf("max deviation = %d, want 90", s.MaxDeviationNs)
	}
}

func TestAlignNoAudio(t *testing.T) {
	events := []merge.Event{{TimestampNs: 0, Track: 1, Kind: "video"}}
	if s := Align(events); s.Pairs != 0 || s.MaxDeviationNs != 0 {
		t.Fatalf("summary = %+v", s)
	}
}
