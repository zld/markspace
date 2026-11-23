package subtitle

import (
	"strings"
	"testing"

	"mediqc/internal/clock"
	"mediqc/internal/fault"
)

const goodVTT = `WEBVTT

intro
00:01.000 --> 00:03.500
Hello world

00:01:00.000 --> 01:00:02.000 align:start position:0%
Second cue
`

func TestParseVTTGood(t *testing.T) {
	tl, err := ParseVTT(strings.NewReader(goodVTT))
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Cues) != 2 {
		t.Fatalf("want 2 cues, got %d", len(tl.Cues))
	}
	if tl.Cues[0].Start != clock.Timescale {
		t.Fatalf("cue 1 start: %d", tl.Cues[0].Start)
	}
	if tl.Cues[1].End != 3600*clock.Timescale+2*clock.Timescale {
		t.Fatalf("cue 2 end: %d", tl.Cues[1].End)
	}
}

func TestVTTNoHeader(t *testing.T) {
	_, err := ParseVTT(strings.NewReader("00:01.000 --> 00:02.000\nx\n"))
	if !fault.IsCode(err, fault.CodeBadTimecode) {
		t.Fatalf("want E_BAD_TIMECODE, got %v", err)
	}
}

func TestVTTTruncated(t *testing.T) {
	_, err := ParseVTT(strings.NewReader("WEBVTT\n\ncue-id\n"))
	if !fault.IsCode(err, fault.CodeTruncatedInput) {
		t.Fatalf("want E_TRUNCATED_INPUT, got %v", err)
	}
	_, err = ParseVTT(strings.NewReader("WEBVTT\n\n00:01.000 --> 00:02.000"))
	if !fault.IsCode(err, fault.CodeTruncatedInput) {
		t.Fatalf("want E_TRUNCATED_INPUT for empty payload at EOF, got %v", err)
	}
}

func TestVTTNegativeDuration(t *testing.T) {
	in := "WEBVTT\n\n00:05.000 --> 00:04.000\nbackwards\n"
	_, err := ParseVTT(strings.NewReader(in))
	if !fault.IsCode(err, fault.CodeNegativeDuration) {
		t.Fatalf("want E_NEGATIVE_DURATION, got %v", err)
	}
}

func TestParseDispatch(t *testing.T) {
	if _, err := Parse("srt", strings.NewReader(goodSRT)); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse("vtt", strings.NewReader(goodVTT)); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse("ttml", strings.NewReader("")); !fault.IsCode(err, fault.CodeBadTimecode) {
		t.Fatalf("want E_BAD_TIMECODE for unknown format, got %v", err)
	}
}
