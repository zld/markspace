package metadata

import (
	"testing"

	"github.com/zld/avpipe/internal/clock"
	"github.com/zld/avpipe/internal/fault"
)

func TestParseSRTAndDedupe(t *testing.T) {
	srt := []byte("1\n00:00:01,000 --> 00:00:02,500\nHello\n\n2\n00:00:02,500 --> 00:00:04,000\nWorld\n")
	events, err := ParseSRT(srt)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events=%d want 2", len(events))
	}
	if events[0].Start.Ticks != 1000 || events[0].End.Ticks != 2500 {
		t.Fatalf("bad timing: %+v", events[0])
	}

	dup := events[0]
	scaled := dup
	scaled.Start = dup.Start.In(90000)
	if scaled.Start.Ticks != 90000 {
		t.Fatalf("scaled ticks=%d want 90000", scaled.Start.Ticks)
	}
	out := SortAndDedupe([]Event{events[1], dup, scaled, events[0]})
	if len(out) != 2 {
		t.Fatalf("dedupe len=%d want 2: %+v", len(out), out)
	}
	if out[0].Text != "Hello" || out[1].Text != "World" {
		t.Fatalf("sort order wrong: %q then %q", out[0].Text, out[1].Text)
	}
}

func TestTimedRecordsRoundTripAndTruncationOffsets(t *testing.T) {
	end := clock.NewTimestamp(3000, 1000)
	want := []Event{{
		Track: "subtitle", Kind: "cue", Start: clock.NewTimestamp(1000, 1000),
		End: &end, Text: "cache me",
	}}
	data := BuildTimedRecords(want)
	got, err := ParseTimedRecords(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "cache me" || got[0].Digest == "" {
		t.Fatalf("roundtrip=%+v", got)
	}

	// Every prefix must fail safely and name the first absent byte, never panic.
	for n := 0; n < len(data); n++ {
		prefix := data[:n]
		_, err := ParseTimedRecords(prefix)
		if err == nil {
			// A prefix ending exactly at a record boundary is a valid,
			// possibly empty, event set.
			continue
		}
		fe, ok := fault.As(err)
		if !ok {
			t.Fatalf("prefix %d error is not structured: %v", n, err)
		}
		if fe.FirstUnreadable != int64(n) {
			t.Fatalf("prefix %d FirstUnreadable=%d want %d", n, fe.FirstUnreadable, n)
		}
	}
}

func TestID3RoundTripAndTruncatedOffset(t *testing.T) {
	e := Event{Track: "id3", Kind: "id3-priv", Start: clock.NewTimestamp(2000, 1000), Text: "chapter-2"}
	data := BuildID3Priv(e)
	got, err := ParseID3Priv(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "chapter-2" || got[0].Start.Ticks != 2000 {
		t.Fatalf("ID3 roundtrip=%+v", got)
	}
	_, err = ParseID3Priv(data[:12])
	if err == nil {
		t.Fatal("expected truncation error")
	}
	fe, ok := fault.As(err)
	if !ok || fe.FirstUnreadable != 12 {
		t.Fatalf("truncation detail=%#v ok=%v", fe, ok)
	}
}

func TestSEIRoundTrip(t *testing.T) {
	end := clock.NewTimestamp(2500, 1000)
	e := Event{Track: "sei", Kind: "sei-user-data", Start: clock.NewTimestamp(1500, 1000), End: &end, Text: "sei-marker"}
	data := BuildSEI(e)
	got, err := ParseSEI(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "sei-marker" || got[0].Start.Ticks != 1500 {
		t.Fatalf("SEI roundtrip=%+v err=%v", got, err)
	}
}
