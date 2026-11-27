package subtitle

import (
	"testing"

	"media.rebuild/internal/model"
)

const goodVTT = `WEBVTT - review sample
Kind: captions

NOTE this note is ignored
and so is this line

STYLE
::cue { color: white }

intro
00:00:01.000 --> 00:00:02.500 line:90% align:start
Hello <b>world</b>

00:00:02.500 --> 00:00:04.000
Back-to-back cue
`

func TestParseVTTBasic(t *testing.T) {
	tr, err := Parse(KindVTT, "good.vtt", goodVTT)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Errors) != 0 {
		t.Fatalf("errors %v", tr.Errors)
	}
	if len(tr.Cues) != 2 {
		t.Fatalf("cues %+v", tr.Cues)
	}
	if tr.Cues[0].Text != "Hello <b>world</b>" {
		t.Fatalf("markup not preserved: %q", tr.Cues[0].Text)
	}
	if tr.Cues[0].Start != 1_000_000_000 || tr.Cues[0].End != 2_500_000_000 {
		t.Fatalf("times %+v", tr.Cues[0])
	}
	// Cue lines: NOTE at 4, STYLE 7, first cue id 10, timing 11, second
	// timing 14.
	if tr.Cues[0].Line != 10 {
		t.Fatalf("cue line %d, want 10", tr.Cues[0].Line)
	}
	if tr.Cues[1].Line != 14 {
		t.Fatalf("second cue line %d, want 14", tr.Cues[1].Line)
	}
}

func TestParseVTTMissingSignatureContinues(t *testing.T) {
	tr, _ := Parse(KindVTT, "nosig.vtt", "00:00:01.000 --> 00:00:02.000\nText\n")
	if len(tr.Cues) != 1 {
		t.Fatalf("cue lost: %+v", tr.Cues)
	}
	if len(tr.Errors) != 1 || !model.IsCode(tr.Errors[0], model.CodeSubtitleSyntax) {
		t.Fatalf("signature error missing: %v", tr.Errors)
	}
	if tr.Errors[0].Source.Line != 1 {
		t.Fatalf("signature error line %d", tr.Errors[0].Source.Line)
	}
}

func TestParseVTTRejectsMissingMilliseconds(t *testing.T) {
	tr, _ := Parse(KindVTT, "bad.vtt", "WEBVTT\n\n00:00:01 --> 00:00:02\nText\n")
	if len(tr.Cues) != 0 {
		t.Fatalf("malformed timing accepted: %+v", tr.Cues)
	}
	if len(tr.Errors) != 1 {
		t.Fatalf("want one error, got %v", tr.Errors)
	}
}

func TestParseVTTZeroDurationIsError(t *testing.T) {
	tr, _ := Parse(KindVTT, "zero.vtt", "WEBVTT\n\n00:00:01.000 --> 00:00:01.000\nBlink\n")
	if len(tr.Cues) != 0 || len(tr.Errors) != 1 {
		t.Fatalf("cues %+v errors %v", tr.Cues, tr.Errors)
	}
	if !model.IsCode(tr.Errors[0], model.CodeSubtitleTiming) {
		t.Fatalf("code %s", tr.Errors[0].Code)
	}
}

func TestOverlapDetection(t *testing.T) {
	cues := []Cue{
		{Start: 0, End: 10},
		{Start: 5, End: 15},
		{Start: 20, End: 30},
		{Start: 12, End: 22},
	}
	pairs := Overlap(cues)
	want := [][2]int{{0, 1}, {2, 3}, {1, 3}}
	if len(pairs) != len(want) {
		t.Fatalf("pairs %v", pairs)
	}
	for i := range want {
		if pairs[i] != want[i] {
			t.Fatalf("pairs %v, want %v", pairs, want)
		}
	}
	// Touching intervals are not overlaps.
	if pairs := Overlap([]Cue{{Start: 0, End: 5}, {Start: 5, End: 9}}); len(pairs) != 0 {
		t.Fatalf("touching cues flagged: %v", pairs)
	}
}
