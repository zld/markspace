package subtitle

import (
	"strings"
	"testing"

	"mediaaudit/internal/model"
)

const sampleSRT = `1
00:00:01,000 --> 00:00:02,500
Hello

2
00:00:03,000 --> 00:00:04,000
World
line two
`

func TestParseSRT(t *testing.T) {
	doc := Parse([]byte(sampleSRT), "a.srt", FormatSRT, Options{})
	if len(doc.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", doc.Errors)
	}
	if len(doc.Cues) != 2 {
		t.Fatalf("cues=%d", len(doc.Cues))
	}
	c := doc.Cues[0]
	if !c.Start.Equal(model.MustRat(1, 1)) || !c.End.Equal(model.MustRat(5, 2)) {
		t.Fatalf("cue 0 times %s..%s", c.Start, c.End)
	}
	if c.StartText != "00:00:01,000" || c.EndText != "00:00:02,500" {
		t.Fatalf("raw boundary tokens lost: %q %q", c.StartText, c.EndText)
	}
	if doc.Cues[1].Text() != "World\nline two" {
		t.Fatalf("body %q", doc.Cues[1].Text())
	}
	// Cues point at their timing line, which is where boundary errors surface.
	if doc.Cues[0].Pos.Line != 2 || doc.Cues[1].Pos.Line != 6 {
		t.Fatalf("positions %d %d", doc.Cues[0].Pos.Line, doc.Cues[1].Pos.Line)
	}
}

func TestParseSRTCrlfAndBOM(t *testing.T) {
	data := "\xef\xbb\xbf1\r\n00:00:00,500 --> 00:00:01,000\r\nHi\r\n"
	doc := Parse([]byte(data), "crlf.srt", FormatSRT, Options{})
	if len(doc.Errors) != 0 || len(doc.Cues) != 1 {
		t.Fatalf("errors=%v cues=%d", doc.Errors, len(doc.Cues))
	}
	if !doc.Cues[0].Start.Equal(model.MustRat(1, 2)) {
		t.Fatalf("start %s", doc.Cues[0].Start)
	}
}

func TestSubtitleAnomaliesAreClassified(t *testing.T) {
	cases := []struct {
		in   string
		code model.ErrorCode
		line int
	}{
		{"1\n00:00:05,000 --> 00:00:05,000\nzero\n", model.CodeSubtitleZero, 2},
		{"1\n00:00:06,000 --> 00:00:05,000\nback\n", model.CodeSubtitleReversed, 2},
		{"1\n00:00:0,000 --> 00:00:05,000\nbad\n", model.CodeSubtitleMalformed, 2},
		{"1\n00:00:01.000 --> 00:00:02.000\ncomma\n", model.CodeSubtitleMalformed, 2},
		{"x\n00:00:01,000 --> 00:00:02,000\nid\n", model.CodeSubtitleMalformed, 1},
		{"1\n00:00:01,000 --> 00:00:02,000\nok\n\n2\n00:00:03,000 --> 00:00:03,000\nz\n", model.CodeSubtitleZero, 6},
	}
	for _, c := range cases {
		doc := Parse([]byte(c.in), "x.srt", FormatSRT, Options{})
		if len(doc.Errors) != 1 {
			t.Fatalf("%q: errors=%v", c.in, doc.Errors)
		}
		e := doc.Errors[0]
		if e.Code != c.code || e.Pos.Line != c.line {
			t.Fatalf("%q: got %s at line %d, want %s at %d", c.in, e.Code, e.Pos.Line, c.code, c.line)
		}
	}
}

func TestOverlapReporting(t *testing.T) {
	srt := "1\n00:00:01,000 --> 00:00:03,000\nA\n\n2\n00:00:02,000 --> 00:00:04,000\nB\n"
	doc := Parse([]byte(srt), "o.srt", FormatSRT, Options{})
	if len(doc.Cues) != 2 {
		t.Fatalf("both cues must survive overlap: %v", doc.Errors)
	}
	ov, errs := Overlaps(doc.Cues)
	if len(ov) != 1 || len(errs) != 1 {
		t.Fatalf("overlap=%v errs=%v", ov, errs)
	}
	if errs[0].Code != model.CodeSubtitleOverlap || errs[0].Pos.Line != 6 {
		t.Fatalf("overlap diagnostic %+v", errs[0])
	}
	if !errs[0].Recoverable() {
		t.Fatal("overlap is a recoverable per-cue problem")
	}
}

const sampleVTT = `WEBVTT

NOTE this is a comment
spanning lines

1
00:00:01.000 --> 00:00:02.500 align:start
Hello

00:03.000 --> 00:04.000
Short form
`

func TestParseVTT(t *testing.T) {
	doc := Parse([]byte(sampleVTT), "a.vtt", FormatVTT, Options{})
	if len(doc.Errors) != 0 {
		t.Fatalf("errors: %v", doc.Errors)
	}
	if len(doc.Cues) != 2 {
		t.Fatalf("cues=%d", len(doc.Cues))
	}
	if !doc.Cues[0].Start.Equal(model.RatFromInt(1)) || !doc.Cues[0].End.Equal(model.MustRat(5, 2)) {
		t.Fatalf("cue0 %s..%s", doc.Cues[0].Start, doc.Cues[0].End)
	}
	if !doc.Cues[1].Start.Equal(model.RatFromInt(3)) {
		t.Fatalf("MM:SS short form = %s", doc.Cues[1].Start)
	}
}

func TestVTTTimestampMapShift(t *testing.T) {
	vtt := "WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:900000\n\n00:00:01.000 --> 00:00:02.000\nA\n"
	doc := Parse([]byte(vtt), "m.vtt", FormatVTT, Options{})
	if len(doc.Errors) != 0 {
		t.Fatalf("errors: %v", doc.Errors)
	}
	if !doc.MPEGTSOffset.Equal(model.RatFromInt(10)) {
		t.Fatalf("offset %s, want 10", doc.MPEGTSOffset)
	}
	if !doc.Cues[0].Start.Equal(model.RatFromInt(11)) {
		t.Fatalf("shifted start %s, want 11", doc.Cues[0].Start)
	}
}

func TestVTTMissingSignature(t *testing.T) {
	doc := Parse([]byte("00:00:01.000 --> 00:00:02.000\nA\n"), "bad.vtt", FormatVTT, Options{})
	if len(doc.Errors) == 0 || !strings.Contains(doc.Errors[0].Message, "WEBVTT") {
		t.Fatalf("missing signature not reported: %v", doc.Errors)
	}
}

func TestDetectFormat(t *testing.T) {
	if DetectFormat("x.vtt", nil) != FormatVTT || DetectFormat("x.srt", []byte("WEBVTT")) != FormatSRT {
		t.Fatal("extension must win over content")
	}
	if DetectFormat("x", []byte("WEBVTT\n")) != FormatVTT {
		t.Fatal("content sniffing failed")
	}
}
