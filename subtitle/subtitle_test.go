package subtitle

import (
	"strings"
	"testing"

	"mediarc/model"
)

const goodSRT = "1\r\n" +
	"00:00:01,001 --> 00:00:02,500\r\n" +
	"Hello there\r\n" +
	"second line\r\n" +
	"\r\n" +
	"2\r\n" +
	"00:00:03,000 --> 00:00:04,000\r\n" +
	"General Kenobi\r\n" +
	"\r\n"

func TestParseSRTCuesAndOffsets(t *testing.T) {
	result := ParseSRT([]byte(goodSRT))
	if len(result.Issues) != 0 {
		t.Fatalf("unexpected issues: %v", result.Issues)
	}
	if len(result.Cues) != 2 {
		t.Fatalf("got %d cues, want 2", len(result.Cues))
	}
	first := result.Cues[0]
	if first.Index != 1 || first.Start != 1_001_000_000 || first.End != 2_500_000_000 {
		t.Fatalf("first cue = %+v", first)
	}
	if got := first.Text(); got != "Hello there\nsecond line" {
		t.Fatalf("first cue text = %q", got)
	}
	if first.Lines[0].Number != 3 || first.HeaderLine != 2 {
		t.Fatalf("line numbers: header=%d payload=%d", first.HeaderLine, first.Lines[0].Number)
	}
	// The cue's byte span must quote its own text back out of the source.
	src := []byte(goodSRT)
	span := string(src[first.StartOffset:first.EndOffset])
	if !strings.Contains(span, "Hello there") || strings.Contains(span, "General") {
		t.Fatalf("cue span = %q", span)
	}
	// Payload line offsets point at exactly that line.
	payload := string(src[first.Lines[0].Offset : first.Lines[0].Offset+int64(len(first.Lines[0].Text))])
	if payload != "Hello there" {
		t.Fatalf("payload offset reads %q", payload)
	}
}

func TestParseSRTRecoversAfterBadCue(t *testing.T) {
	src := "1\n" +
		"00:00:00,000 --> 00:00:01,000\n" +
		"ok\n\n" +
		"2\n" +
		"00:00:01,000 -> 00:00:02,000\n" +
		"broken arrow\n\n" +
		"3\n" +
		"00:00:02,000 --> 00:00:01,000\n" +
		"backwards\n\n" +
		"4\n" +
		"00:00:05,000 --> 00:00:06,000\n" +
		"recovered\n"
	result := ParseSRT([]byte(src))
	if len(result.Cues) != 2 {
		t.Fatalf("recovered %d cues, want 2: %+v", len(result.Cues), result.Cues)
	}
	if len(result.Issues) != 2 {
		t.Fatalf("got %d issues, want 2: %v", len(result.Issues), result.Issues)
	}
	for _, issue := range result.Issues {
		if !model.IsCode(issue, model.CodeInvalidCue) {
			t.Fatalf("issue code = %s", issue.Code)
		}
		if issue.Offset < 0 {
			t.Fatalf("issue lacks a byte offset: %v", issue)
		}
	}
	if result.Cues[1].Text() != "recovered" {
		t.Fatalf("second recovered cue = %q", result.Cues[1].Text())
	}
	// Issue offsets must be inside the source and ordered.
	if result.Issues[0].Offset >= result.Issues[1].Offset {
		t.Fatalf("issue offsets not ordered: %d, %d", result.Issues[0].Offset, result.Issues[1].Offset)
	}
}

func TestParseSRTMissingSeparator(t *testing.T) {
	src := "1\n00:00:00,000 --> 00:00:01,000\none\n2\n00:00:01,000 --> 00:00:02,000\ntwo\n"
	result := ParseSRT([]byte(src))
	if len(result.Cues) != 2 {
		t.Fatalf("got %d cues, want 2: %v", len(result.Cues), result.Issues)
	}
	if result.Cues[0].Text() != "one" || result.Cues[1].Text() != "two" {
		t.Fatalf("cues = %q, %q", result.Cues[0].Text(), result.Cues[1].Text())
	}
}

func TestParseSRTGarbageBetweenCues(t *testing.T) {
	src := goodSRT + "\nthis line is junk\nand this\n\n"
	result := ParseSRT([]byte(src))
	if len(result.Cues) != 2 {
		t.Fatalf("got %d cues", len(result.Cues))
	}
	if len(result.Issues) != 1 || !model.IsCode(result.Issues[0], model.CodeInvalidCue) {
		t.Fatalf("issues = %v", result.Issues)
	}
	if !strings.Contains(result.Issues[0].Detail, "timing") {
		t.Fatalf("detail = %q", result.Issues[0].Detail)
	}
}

func TestParseSRTEmptyAndBOM(t *testing.T) {
	if got := ParseSRT(nil); len(got.Cues) != 0 || len(got.Issues) != 0 {
		t.Fatalf("empty input produced %+v", got)
	}
	withBOM := append([]byte{0xEF, 0xBB, 0xBF}, []byte(goodSRT)...)
	result := ParseSRT(withBOM)
	if len(result.Cues) != 2 {
		t.Fatalf("BOM input cues = %d, issues = %v", len(result.Cues), result.Issues)
	}
	// BOM must be skipped, so offsets still address real bytes.
	off := result.Cues[0].Lines[0].Offset
	if string(withBOM[off:off+int64(len("Hello there"))]) != "Hello there" {
		t.Fatalf("BOM-shifted offset points at %q", withBOM[off:off+11])
	}
}

func TestParseSRTChronologyIsNotSilentlyFixed(t *testing.T) {
	src := "1\n00:00:05,000 --> 00:00:04,000\nbad\n\n2\n00:00:00,000 --> 00:00:01,000\ngood\n"
	result := ParseSRT([]byte(src))
	if len(result.Cues) != 1 || result.Cues[0].Text() != "good" {
		t.Fatalf("cues = %+v", result.Cues)
	}
	if result.Cues[0].Start != 0 || result.Cues[0].End != 1_000_000_000 {
		t.Fatalf("cue times changed: %+v", result.Cues[0])
	}
}

const goodVTT = "WEBVTT\n" +
	"Kind: captions\n" +
	"Language: en\n" +
	"\n" +
	"NOTE this is a comment\n" +
	"spanning two lines\n" +
	"\n" +
	"STYLE\n" +
	"::cue { color: red }\n" +
	"\n" +
	"intro\n" +
	"00:00:01.000 --> 00:00:02.000 align:start line:0\n" +
	"<v Bob>Hi</v>\n" +
	"\n" +
	"00:00:03.000 --> 00:00:04.000\n" +
	"plain\n"

func TestParseVTT(t *testing.T) {
	result := ParseWebVTT([]byte(goodVTT))
	if len(result.Issues) != 0 {
		t.Fatalf("issues = %v", result.Issues)
	}
	if len(result.Cues) != 2 {
		t.Fatalf("got %d cues: %+v", len(result.Cues), result.Cues)
	}
	if result.Cues[0].Start != 1_000_000_000 || result.Cues[0].End != 2_000_000_000 {
		t.Fatalf("first cue times = %d..%d", result.Cues[0].Start, result.Cues[0].End)
	}
	if result.Cues[0].Settings != "align:start line:0" {
		t.Fatalf("settings = %q", result.Cues[0].Settings)
	}
	if result.Cues[0].Text() != "<v Bob>Hi</v>" {
		t.Fatalf("first text = %q", result.Cues[0].Text())
	}
	if result.Cues[1].Index != 0 || result.Cues[1].Text() != "plain" {
		t.Fatalf("second cue = %+v", result.Cues[1])
	}
}

func TestParseVTTMissingHeaderAndBadSettings(t *testing.T) {
	src := "00:00:01.000 --> 00:00:02.000 align=bogus\nx\n\n00:00:03.000 --> 00:00:04.000\nok\n"
	result := ParseWebVTT([]byte(src))
	if len(result.Cues) != 1 {
		t.Fatalf("got %d cues, want 1", len(result.Cues))
	}
	if len(result.Issues) != 2 {
		t.Fatalf("got %d issues: %v", len(result.Issues), result.Issues)
	}
	if !model.IsCode(result.Issues[0], model.CodeInvalidCue) {
		t.Fatalf("first issue = %v", result.Issues[0])
	}
}

func TestVTTShortClocks(t *testing.T) {
	src := "WEBVTT\n\n00:01.500 --> 00:02.000\nshort form\n"
	result := ParseWebVTT([]byte(src))
	if len(result.Cues) != 1 {
		t.Fatalf("cues = %+v issues=%v", result.Cues, result.Issues)
	}
	if result.Cues[0].Start != 1_500_000_000 {
		t.Fatalf("start = %d", result.Cues[0].Start)
	}
}

func TestDetectAndDispatch(t *testing.T) {
	if got := Detect([]byte(goodVTT)); got != FormatVTT {
		t.Fatalf("Detect(vtt) = %v", got)
	}
	if got := Detect([]byte(goodSRT)); got != FormatSRT {
		t.Fatalf("Detect(srt) = %v", got)
	}
	if got := Parse([]byte("\xEF\xBB\xBFWEBVTT\n\n00:00:00.000 --> 00:00:01.000\na\n")); got.Format != FormatVTT || len(got.Cues) != 1 {
		t.Fatalf("Parse vtt with BOM = %+v", got)
	}
}

func TestChecksumDeterministicAndSensitive(t *testing.T) {
	a := ParseSRT([]byte(goodSRT))
	b := ParseSRT([]byte(goodSRT))
	if a.Checksum == 0 || a.Checksum != b.Checksum {
		t.Fatalf("checksums %x and %x", a.Checksum, b.Checksum)
	}
	changed := strings.Replace(goodSRT, "Hello there", "Hello there!", 1)
	c := ParseSRT([]byte(changed))
	if c.Checksum == a.Checksum {
		t.Fatalf("checksum ignored text change")
	}
}

func TestParseWithFormat(t *testing.T) {
	if got := ParseWithFormat([]byte(goodSRT), FormatVTT); got.Format != FormatVTT {
		t.Fatalf("format = %v", got.Format)
	}
}
