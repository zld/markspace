package events

import (
	"bytes"
	"errors"
	"testing"
)

func TestCompareEventsIsTotalAndDeterministic(t *testing.T) {
	base := []Event{
		{Track: TrackVideo, Kind: KindSEI, At: 1000, Seq: 7, Text: "sei"},
		{Track: TrackChapter, Kind: KindChapter, At: 1000, Seq: 2, Text: "ch2"},
		{Track: TrackSubtitle, Kind: KindSubtitle, At: 500, Seq: 9, Text: "sub9"},
		{Track: TrackSubtitle, Kind: KindSubtitle, At: 500, Seq: 3, Text: "sub3"},
	}
	for i := 0; i < 40; i++ {
		// Rotate the arrival order; canonical order must not change.
		rotated := append(append([]Event(nil), base[i%len(base):]...), base[:i%len(base)]...)
		got := SortEvents(rotated)
		want := SortEvents(base)
		for j := range want {
			if CompareEvents(got[j], want[j]) != 0 {
				t.Fatalf("rotation %d position %d: got %v want %v", i, j, got[j], want[j])
			}
		}
	}
}

func TestStateBytesIndependentOfArrivalOrder(t *testing.T) {
	evs := []Event{
		{Track: TrackSubtitle, Kind: KindSubtitle, At: 100, Duration: 200, Seq: 1, Text: "a", Payload: []byte{1}},
		{Track: TrackChapter, Kind: KindChapter, At: 300, Seq: 2, Text: "b"},
		{Track: TrackID3, Kind: KindID3, At: 150, Seq: 3, Payload: []byte{9, 8}},
	}
	one := StateBytes(300, evs, nil)
	shuffled := []Event{evs[2], evs[0], evs[1]}
	two := StateBytes(300, shuffled, nil)
	if !bytes.Equal(one, two) {
		t.Fatalf("state digest depends on arrival order")
	}
}

func TestStateBytesDetectsRealChange(t *testing.T) {
	evs := []Event{{Track: TrackSubtitle, Kind: KindSubtitle, At: 100, Seq: 1, Text: "a"}}
	one := StateBytes(100, evs, nil)
	changed := []Event{{Track: TrackSubtitle, Kind: KindSubtitle, At: 100, Seq: 1, Text: "b"}}
	two := StateBytes(100, changed, nil)
	if bytes.Equal(one, two) {
		t.Fatal("text change must change state digest")
	}
}

func TestUnresolvedGapsExcludedWhenFilled(t *testing.T) {
	gaps := []Gap{
		{Track: TrackRTP, Seq: 5, Filled: false},
		{Track: TrackRTP, Seq: 6, Filled: true},
	}
	got := StateBytes(0, nil, gaps)
	if want := StateBytes(0, nil, gaps[:1]); !bytes.Equal(got, want) {
		t.Fatal("filled gaps must not affect the canonical state")
	}
}

func TestErrorFatalClassification(t *testing.T) {
	cases := []struct {
		code  Code
		fatal bool
	}{
		{CodeInvalidTimescale, true},
		{CodeCapacity, true},
		{CodeWrite, true},
		{CodeTruncated, false},
		{CodeMalformed, false},
		{CodeReplayWindow, false},
	}
	for _, c := range cases {
		e := NewError(c.code, TrackSubtitle, 4, 128, "parse", "x", nil)
		if e.Fatal() != c.fatal {
			t.Fatalf("%s fatal=%v want %v", c.code, e.Fatal(), c.fatal)
		}
		if want := "track=subtitle seq=4 offset=128"; !contains(e.Error(), want) {
			t.Fatalf("%q missing %q", e.Error(), want)
		}
	}
}

func TestErrorUnwrap(t *testing.T) {
	root := errors.New("disk full")
	e := NewError(CodeWrite, TrackVideo, 0, -1, "write", "manifest", root)
	if !errors.Is(e, root) {
		t.Fatal("Unwrap must expose the cause")
	}
	if contains(e.Error(), "offset=") {
		t.Fatal("negative offset must not be rendered")
	}
}

func TestPayloadDigestStable(t *testing.T) {
	a := Event{Payload: []byte("abc")}
	b := Event{Payload: []byte("abc")}
	if a.PayloadDigest() != b.PayloadDigest() {
		t.Fatal("digest must be stable")
	}
	if a.PayloadDigest() == (Event{Payload: []byte("abd")}).PayloadDigest() {
		t.Fatal("digest must differ")
	}
}

func TestFormatTrackSeq(t *testing.T) {
	if got, want := FormatTrackSeq(TrackEmsg, 3, 64), "track=emsg seq=3 offset=64"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func contains(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }
