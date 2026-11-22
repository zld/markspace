package subtitle

import (
	"strings"
	"testing"

	"mediaarchive/internal/diag"
)

func TestSRTBasicAndMicroseconds(t *testing.T) {
	src := "1\n00:00:01,000 --> 00:00:03,500\nHello\nworld\n\n" +
		"2\n00:00:04.000000 --> 00:00:05.250000\nµs cue\n"
	res := Parse(strings.NewReader(src), "a.srt")
	if len(res.Faults) != 0 {
		t.Fatalf("unexpected faults: %v", res.Faults)
	}
	if len(res.Cues) != 2 {
		t.Fatalf("cues = %d", len(res.Cues))
	}
	if res.Cues[0].Text != "Hello\nworld" {
		t.Fatalf("multiline text = %q", res.Cues[0].Text)
	}
	if got := res.Cues[0].Start.Micros(); got != 1_000_000 {
		t.Fatalf("start = %v µs", got)
	}
	if got := res.Cues[1].End.Millis(); got != 5250 {
		t.Fatalf("µs cue end = %v ms", got)
	}
}

func TestSRTBOMOffsetPointsIntoRealFile(t *testing.T) {
	src := "\xEF\xBB\xBF1\n00:00:01,000 --> 00:00:02,000\nok\n\n2\nbad --> nope\nrecovers\n\n3\n00:00:05,000 --> 00:00:06,000\nafter\n"
	res := Parse(strings.NewReader(src), "bom.srt")
	if len(res.Cues) != 2 {
		t.Fatalf("recoverable cues = %d, want 2", len(res.Cues))
	}
	if !res.HasCode(diag.CodeInvalidTime) {
		t.Fatal("bad timing not recorded")
	}
	f, _ := diag.As(res.Err())
	if f.Offset != 39 {
		t.Fatalf("fault offset = %d, want 39 (start of bad block in the BOM-prefixed file)", f.Offset)
	}
	if res.Cues[1].Text != "after" {
		t.Fatalf("scanner did not resume: %+v", res.Cues)
	}
	// Cue offsets are absolute bytes into the original document.
	if res.Cues[0].Offset == 0 {
		t.Fatalf("BOM not accounted for in cue offset")
	}
}

func TestSRTTruncatedTailRecorded(t *testing.T) {
	src := "1\n00:00:01,000 --> 00:00:02,000\ncomplete\n\n2\n00:00:03,000"
	res := Parse(strings.NewReader(src), "trunc.srt")
	if len(res.Cues) != 1 {
		t.Fatalf("cues = %d", len(res.Cues))
	}
	if !res.HasCode(diag.CodeTruncated) {
		t.Fatalf("truncated timing not classified as truncation: %v", res.Faults)
	}
	if res.Bytes != int64(len(src)) {
		t.Fatalf("bytes = %d", res.Bytes)
	}
}

func TestVTTIdentifiersSettingsAndNotes(t *testing.T) {
	src := "WEBVTT - archive\nKind: captions\n\nNOTE ignored block\ncontinues\n\n" +
		"intro\n00:01.000 --> 00:04.000 align:start position:10%\nfirst line\n\n" +
		"00:00:05.000 --> 00:00:06.000\nsecond\n"
	res := Parse(strings.NewReader(src), "a.vtt")
	if len(res.Faults) != 0 {
		t.Fatalf("faults: %v", res.Faults)
	}
	if len(res.Cues) != 2 {
		t.Fatalf("cues = %d", len(res.Cues))
	}
	c := res.Cues[0]
	if c.ID != "intro" || c.Settings != "align:start position:10%" {
		t.Fatalf("id/settings = %q / %q", c.ID, c.Settings)
	}
	if c.Start.Millis() != 1000 || c.End.Millis() != 4000 {
		t.Fatalf("abbreviated timestamp = %v..%v", c.Start, c.End)
	}
}

func TestVTTBadCueKeepsScanning(t *testing.T) {
	src := "WEBVTT\n\n00:00:01.000 --> 99:99:99.000\nbroken\n\nok\n00:00:02.000 --> 00:00:03.000\nrecovered\n"
	res := Parse(strings.NewReader(src), "bad.vtt")
	if len(res.Cues) != 1 || res.Cues[0].ID != "ok" {
		t.Fatalf("recovery failed: %+v", res.Cues)
	}
	f, ok := diag.As(res.Err())
	if !ok || f.Code != diag.CodeInvalidTime || f.Offset != 8 {
		t.Fatalf("fault = %+v", f)
	}
}

func TestZeroLengthCueRejected(t *testing.T) {
	res := Parse(strings.NewReader("WEBVTT\n\n00:00:02.000 --> 00:00:02.000\nzero\n"), "z.vtt")
	if len(res.Cues) != 0 || !res.HasCode(diag.CodeInvalidTime) {
		t.Fatalf("zero cue accepted: %+v", res)
	}
}

func TestTruncatedRecordIsDistinctFromInvalidTime(t *testing.T) {
	// A record cut before the arrow must be classified as truncation, not as
	// an illegal clock value.
	res := Parse(strings.NewReader("1\n00:00:01,000 --> 00:00:02,000\nok\n\n2\n00:00:03,000"), "cut.srt")
	if len(res.Cues) != 1 {
		t.Fatalf("cues = %d", len(res.Cues))
	}
	f, ok := diag.As(res.Err())
	if !ok || f.Code != diag.CodeTruncated {
		t.Fatalf("fault = %+v", res.Faults)
	}
	if f.Offset != 36 {
		t.Fatalf("offset = %d, want 36", f.Offset)
	}

	// A timing line present but with no payload is also truncation.
	res = Parse(strings.NewReader("1\n00:00:01,000 --> 00:00:02,000\n\n2\n00:00:03,000 --> 00:00:04,000\nok\n"), "empty.srt")
	if !res.HasCode(diag.CodeTruncated) {
		t.Fatalf("empty payload not flagged: %v", res.Faults)
	}
	if len(res.Cues) != 2 {
		t.Fatalf("cues = %d", len(res.Cues))
	}
}

func TestVTTBOMAndCRLF(t *testing.T) {
	src := "\xEF\xBB\xBFWEBVTT\r\n\r\n00:00:01.000 --> 00:00:02.000\r\nline one\r\nline two\r\n\r\n"
	res := Parse(strings.NewReader(src), "crlf.vtt")
	if len(res.Faults) != 0 {
		t.Fatalf("faults: %v", res.Faults)
	}
	if len(res.Cues) != 1 || res.Cues[0].Text != "line one\nline two" {
		t.Fatalf("cue = %+v", res.Cues)
	}
	if res.Cues[0].Offset <= 3 {
		t.Fatalf("offset = %d, BOM not included", res.Cues[0].Offset)
	}
}
