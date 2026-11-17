package codec

import (
	"strings"
	"testing"
	"time"

	"subtitle/internal/cue"
	"subtitle/internal/parser"
	"subtitle/internal/timebase"
)

func sampleCues() []OutputCue {
	return []OutputCue{
		{Frames: timebase.Frames{Start: 0, End: 24}, Text: "first line\nsecond line"},
		{Frames: timebase.Frames{Start: 24, End: 48}, Text: "hello, world {\\i1}tag"},
	}
}

func rate() timebase.Rate { return timebase.Rate{Num: 24000, Den: 1001} }

func TestSerializeSRT(t *testing.T) {
	out, err := Serialize(sampleCues(), cue.FormatSRT, rate())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "00:00:00,000 --> 00:00:01,001") {
		t.Fatalf("unexpected srt timing:\n%s", out)
	}
	if !strings.Contains(out, "first line\nsecond line") {
		t.Fatalf("multi-line body lost:\n%s", out)
	}
	doc, err := parser.ParseSRT("rt.srt", out)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Cues) != 2 || doc.Cues[0].Text != sampleCues()[0].Text {
		t.Fatalf("round trip mismatch: %+v", doc.Cues)
	}
}

func TestSerializeVTT(t *testing.T) {
	out, err := Serialize(sampleCues(), cue.FormatVTT, rate())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "WEBVTT\n") || !strings.Contains(out, "00:00:00.000 -->") {
		t.Fatalf("unexpected vtt output:\n%s", out)
	}
	doc, err := parser.ParseVTT("rt.vtt", out)
	if err != nil || len(doc.Cues) != 2 {
		t.Fatalf("vtt round trip: %v %+v", err, doc)
	}
}

func TestSerializeASSCentiseconds(t *testing.T) {
	out, err := Serialize(sampleCues(), cue.FormatASS, rate())
	if err != nil {
		t.Fatal(err)
	}
	// Frame 24 begins at 1.001 s -> centiseconds round to 10 (1.00 s).
	if !strings.Contains(out, "Dialogue: 0,0:00:01.00,0:00:02.00,Default") {
		t.Fatalf("ass timing wrong:\n%s", out)
	}
	if !strings.Contains(out, "first line\\Nsecond line") {
		t.Fatalf("ass hard newline lost:\n%s", out)
	}
	doc, err := parser.ParseASS("rt.ass", out)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Cues) != 2 || doc.Cues[0].Text != "first line\nsecond line" {
		t.Fatalf("ass round trip: %+v", doc.Cues)
	}
	if doc.Cues[1].Start != time.Second {
		t.Fatalf("ass start = %s, want 1s", doc.Cues[1].Start)
	}
}

func TestSerializeUnknownFormat(t *testing.T) {
	if _, err := Serialize(nil, cue.Format("xyz"), rate()); err == nil {
		t.Fatal("expected format error")
	}
}
