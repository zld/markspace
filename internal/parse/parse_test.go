package parse

import (
	"strings"
	"testing"

	"subsync/internal/model"
)

func TestSRTParse(t *testing.T) {
	in := "1\n00:00:01,000 --> 00:00:04,000\nhello\nworld\n\n" +
		"2\n00:00:05,500 --> 00:00:06,750 X1:1 X2:2\nsecond\n"
	doc, err := (srtParser{}).Parse(in, "default", model.ParseFail)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Cues) != 2 {
		t.Fatalf("cues = %d, want 2", len(doc.Cues))
	}
	c := doc.Cues[0]
	if c.ID != "1" || c.Text != "hello\nworld" || c.Track != "default" {
		t.Errorf("cue 0 fields wrong: %+v", c)
	}
	if c.Start.Milliseconds() != 1000 || c.End.Milliseconds() != 4000 {
		t.Errorf("timing wrong: %s..%s", c.Start, c.End)
	}
	if doc.Cues[1].End.Milliseconds() != 6750 {
		t.Errorf("coordinates not stripped, end = %s", doc.Cues[1].End)
	}
}

func TestSRTParseFailReportsLine(t *testing.T) {
	in := "1\nnot a timing line\nbody\n\n2\n00:00:01,000 --> 00:00:02,000\nok\n"
	_, err := (srtParser{}).Parse(in, "default", model.ParseFail)
	pe, ok := err.(*model.ParseError)
	if !ok {
		t.Fatalf("expected *ParseError, got %T %v", err, err)
	}
	if pe.Line != 2 {
		t.Errorf("error line = %d, want 2", pe.Line)
	}
}

func TestSRTParseSkip(t *testing.T) {
	in := "1\nbad timing\nbody\n\n2\n00:00:01,000 --> 00:00:02,000\nok\n"
	doc, err := (srtParser{}).Parse(in, "default", model.ParseSkip)
	if err == nil {
		t.Fatal("skip mode must still return the collected ParseErrors")
	}
	if len(doc.Cues) != 1 || doc.Cues[0].ID != "2" {
		t.Errorf("good cue not retained in skip mode: %+v", doc.Cues)
	}
}

func TestVTTParse(t *testing.T) {
	in := "WEBVTT\n\n" +
		"cue-1\n00:00:01.000 --> 00:00:02.000 align:start position:0%\nfirst &amp; line\n\n" +
		"NOTE this is a comment\n\n" +
		"00:00:03.000 --> 00:00:04.000\nsecond\n"
	doc, err := (vttParser{}).Parse(in, "default", model.ParseFail)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Cues) != 2 {
		t.Fatalf("cues = %d, want 2", len(doc.Cues))
	}
	if doc.Cues[0].ID != "cue-1" || doc.Cues[0].Text != "first & line" {
		t.Errorf("vtt cue wrong: %+v", doc.Cues[0])
	}
	if doc.Cues[1].Start.Milliseconds() != 3000 {
		t.Errorf("auto-id cue timing wrong: %+v", doc.Cues[1])
	}
}

func TestASSParse(t *testing.T) {
	in := "[Script Info]\nTitle: t\n\n" +
		"[V4+ Styles]\n" +
		"Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n" +
		"Style: Title,Arial,28,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,-1,0,0,0,100,100,0,0,1,2,2,2,10,10,10,1\n\n" +
		"[Events]\n" +
		"Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		"Dialogue: 0,0:00:01.00,0:00:02.50,Title,,0,0,0,,first line\\Nsecond line\n" +
		"Dialogue: 1,0:00:03.00,0:00:04.00,Default,,0,0,0,,plain, with comma\n"
	doc, err := (assParser{}).Parse(in, "default", model.ParseFail)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Cues) != 2 {
		t.Fatalf("cues = %d", len(doc.Cues))
	}
	c := doc.Cues[0]
	if c.Style != "Title" || c.Layer != 0 || c.Text != "first line\nsecond line" {
		t.Errorf("ass cue fields wrong: %+v", c)
	}
	if c.Start.Milliseconds() != 1000 || c.End.Milliseconds() != 2500 {
		t.Errorf("ass timing wrong: %s..%s", c.Start, c.End)
	}
	if doc.Cues[1].Layer != 1 || !strings.Contains(doc.Cues[1].Text, "with comma") {
		t.Errorf("layer/text comma handling wrong: %+v", doc.Cues[1])
	}
	if _, ok := doc.Styles["Title"]; !ok {
		t.Error("style definition not retained")
	}
}

func TestASSMissingEventsFormat(t *testing.T) {
	_, err := (assParser{}).Parse("[Script Info]\nTitle: x\n", "default", model.ParseFail)
	if err == nil || !strings.Contains(err.Error(), "Events") {
		t.Errorf("expected missing-format error, got %v", err)
	}
}

func TestTimestampVariants(t *testing.T) {
	cases := map[string]int64{
		"01:02:03,500": 3723500,
		"01:02:03.500": 3723500,
		"1:02:03.05":   3723050,
	}
	for in, wantMS := range cases {
		d, err := parseTimestamp(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if d.Milliseconds() != wantMS {
			t.Errorf("%s = %d ms, want %d", in, d.Milliseconds(), wantMS)
		}
	}
}
