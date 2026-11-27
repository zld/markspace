package subtitle

import (
	"testing"

	"media.rebuild/internal/model"
)

const goodSRT = `1
00:00:01,000 --> 00:00:02,500
First line

2
00:00:03,000 --> 00:00:04,000
Second cue
wraps two lines
`

const brokenSRT = `1
00:00:01,000 --> 00:00:02,000
Good before

2
00:00:02,,000 --> 00:00:03,000
Malformed timestamp

not a cue at all

3
00:00:04,000 --> 00:00:03,000
Ends before it starts

4
00:00:05,000 --> 00:00:06,000
Good after
`

const emptyCueSRT = `1
00:00:01,000 --> 00:00:02,000


2
00:00:02,000 --> 00:00:03,000
Survivor
`

func TestParseSRTBasic(t *testing.T) {
	tr, err := Parse(KindSRT, "good.srt", goodSRT)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Cues) != 2 || len(tr.Errors) != 0 {
		t.Fatalf("cues=%d errors=%v", len(tr.Cues), tr.Errors)
	}
	c := tr.Cues[0]
	if c.Start != 1_000_000_000 || c.End != 2_500_000_000 {
		t.Fatalf("times %d %d", c.Start, c.End)
	}
	if c.Line != 1 || c.Source != "good.srt" || c.Index != 0 {
		t.Fatalf("cue metadata %+v", c)
	}
	if tr.Cues[1].Text != "Second cue\nwraps two lines" {
		t.Fatalf("multiline body %q", tr.Cues[1].Text)
	}
}

func TestParseSRTKeepsGoingAfterErrors(t *testing.T) {
	tr, _ := Parse(KindSRT, "broken.srt", brokenSRT)
	if len(tr.Cues) != 2 {
		t.Fatalf("want 2 survivors, got %d: %+v", len(tr.Cues), tr.Cues)
	}
	if tr.Cues[0].Text != "Good before" || tr.Cues[1].Text != "Good after" {
		t.Fatalf("survivors %q %q", tr.Cues[0].Text, tr.Cues[1].Text)
	}
	if len(tr.Errors) != 3 {
		t.Fatalf("want 3 errors, got %d: %v", len(tr.Errors), tr.Errors)
	}
	// Errors point at the cue's first line: 6 (bad timing), 9 (a block with
	// no timing line) and 11 (inverted interval).
	wantLines := []int{6, 9, 11}
	for i, want := range wantLines {
		if tr.Errors[i].Source.Line != want {
			t.Errorf("error %d line %d, want %d (%v)", i, tr.Errors[i].Source.Line, want, tr.Errors[i])
		}
		if tr.Errors[i].Source.Name != "broken.srt" {
			t.Errorf("error %d lacks source name", i)
		}
	}
}

func TestParseSRTEmptyCueIsError(t *testing.T) {
	tr, _ := Parse(KindSRT, "empty.srt", emptyCueSRT)
	if len(tr.Cues) != 1 || tr.Cues[0].Text != "Survivor" {
		t.Fatalf("cues %+v", tr.Cues)
	}
	if len(tr.Errors) != 1 || !model.IsCode(tr.Errors[0], model.CodeSubtitleSyntax) {
		t.Fatalf("errors %v", tr.Errors)
	}
}

func TestParseSRTSortsAndIndexesDeterministically(t *testing.T) {
	text := `2
00:00:05,000 --> 00:00:06,000
later

1
00:00:01,000 --> 00:00:02,000
earlier
`
	first, _ := Parse(KindSRT, "s.srt", text)
	second, _ := Parse(KindSRT, "s.srt", text)
	if len(first.Cues) != 2 || first.Cues[0].Text != "earlier" || first.Cues[1].Text != "later" {
		t.Fatalf("order %+v", first.Cues)
	}
	for i := range first.Cues {
		if first.Cues[i].Index != i {
			t.Fatalf("index not renumbered: %+v", first.Cues)
		}
		if first.Cues[i] != second.Cues[i] {
			t.Fatalf("nondeterministic parse at %d", i)
		}
	}
}

func TestParseDispatchUnknownKind(t *testing.T) {
	if _, err := Parse(Kind("ass"), "x", "hi"); !model.IsCode(err, model.CodeInvalidArgument) {
		t.Fatalf("kind accepted: %v", err)
	}
}

func TestEmptyFileReportsError(t *testing.T) {
	tr, _ := Parse(KindSRT, "empty.srt", "  \n\n")
	if len(tr.Errors) != 1 || len(tr.Cues) != 0 {
		t.Fatalf("empty file: %+v %v", tr.Cues, tr.Errors)
	}
}

func TestDetect(t *testing.T) {
	if Detect("\ufeffWEBVTT\n\n") != KindVTT {
		t.Fatal("vtt not detected")
	}
	if Detect(goodSRT) != KindSRT {
		t.Fatal("srt misdetected")
	}
}
