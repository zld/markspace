package parser

import (
	"strings"
	"testing"
	"time"

	"subtitle/internal/cue"
)

func TestParseSRTMultiLine(t *testing.T) {
	src := "1\n00:00:01,000 --> 00:00:02,500\nfirst line\nsecond line\n\n" +
		"2\n00:00:03,000 --> 00:00:04,000\nhello\n"
	doc, err := ParseSRT("scene.srt", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Cues) != 2 || doc.Format != cue.FormatSRT {
		t.Fatalf("got %d cues", len(doc.Cues))
	}
	c := doc.Cues[0]
	if c.Start != time.Second || c.End != 2500*time.Millisecond {
		t.Fatalf("timing %s..%s", c.Start, c.End)
	}
	if c.Text != "first line\nsecond line" || c.SourceLine != 1 {
		t.Fatalf("text=%q line=%d", c.Text, c.SourceLine)
	}
}

func TestParseSRTErrorLine(t *testing.T) {
	src := "1\n00:00:01,000 --> 00:00:02,000\nok\n\n2\nbroken timing\n"
	_, err := ParseSRT("scene.srt", src)
	pe, ok := err.(*Error)
	if !ok || pe.Line != 6 || !strings.Contains(pe.Error(), "scene.srt:6") {
		t.Fatalf("expected line 6 error, got %v", err)
	}
}

func TestParseVTTIdentifierAndSettings(t *testing.T) {
	src := "WEBVTT\n\n" +
		"cue-1\n00:00:01.000 --> 00:00:02.000 align:start position:0%\nhi there\n\n" +
		"00:00:03.000 --> 00:00:04.500\nsecond\nline two\n\n" +
		"NOTE this is a note\nignored line\n"
	doc, err := ParseVTT("scene.vtt", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Cues) != 2 {
		t.Fatalf("got %d cues", len(doc.Cues))
	}
	if doc.Cues[0].ID != "cue-1" {
		t.Fatalf("id=%q", doc.Cues[0].ID)
	}
	if doc.Cues[1].Text != "second\nline two" || doc.Cues[1].End != 4500*time.Millisecond {
		t.Fatalf("cue2=%+v", doc.Cues[1])
	}
}

func TestParseVTTMissingHeader(t *testing.T) {
	if _, err := ParseVTT("x.vtt", "00:00:01.000 --> 00:00:02.000\nx\n"); err == nil {
		t.Fatal("expected header error")
	}
}

const assSample = "[Script Info]\nTitle: demo\n\n" +
	"[Events]\n" +
	"Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
	"Dialogue: 0,0:00:01.00,0:00:02.50,Default,,0,0,0,,hello, world {\\i1}tag\\Nsecond line\n" +
	"Comment: 0,0:00:03.00,0:00:04.00,Default,,0,0,0,,ignored\n" +
	"Dialogue: 0,0:00:05.00,0:00:06.00,Default,,0,0,0,,next cue\n"

func TestParseASSDynamicFormatAndCommaText(t *testing.T) {
	doc, err := ParseASS("scene.ass", assSample)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Cues) != 2 {
		t.Fatalf("got %d cues, comments must be skipped", len(doc.Cues))
	}
	c := doc.Cues[0]
	if c.Start != time.Second || c.End != 2500*time.Millisecond {
		t.Fatalf("timing %s..%s", c.Start, c.End)
	}
	want := "hello, world {\\i1}tag\nsecond line"
	if c.Text != want {
		t.Fatalf("text=%q want %q", c.Text, want)
	}
	if c.SourceLine != 6 {
		t.Fatalf("source line=%d", c.SourceLine)
	}
}

func TestParseASSReorderedColumns(t *testing.T) {
	src := "[Events]\nFormat: Start, End, Text\n" +
		"Dialogue: 0:00:01.00,0:00:02.00,a,b,c still text\n"
	doc, err := ParseASS("x.ass", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Cues) != 1 || doc.Cues[0].Text != "a,b,c still text" {
		t.Fatalf("got %+v", doc.Cues)
	}
}

func TestParseASSBadTimestamp(t *testing.T) {
	src := "[Events]\nFormat: Start, End, Text\nDialogue: 0:00:01.0,bad,text\n"
	_, err := ParseASS("x.ass", src)
	pe, ok := err.(*Error)
	if !ok || pe.Line != 3 {
		t.Fatalf("expected line 3 error, got %v", err)
	}
}

func TestTimestampParsers(t *testing.T) {
	if d, _ := parseSRTTime("01:02:03,456"); d != time.Hour+2*time.Minute+3*time.Second+456*time.Millisecond {
		t.Fatalf("srt time %s", d)
	}
	if d, _ := parseASSTime("1:02:03.45"); d != time.Hour+2*time.Minute+3*time.Second+450*time.Millisecond {
		t.Fatalf("ass time %s", d)
	}
	if _, err := parseSRTTime("nope"); err == nil {
		t.Fatal("expected error")
	}
}
