package subtitle

import (
	"strings"
	"testing"

	"packcheck/internal/clock"
	"packcheck/internal/model"
)

func testRate(t *testing.T) clock.Rate {
	t.Helper()
	r, err := clock.ParseRate("25")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestParseSRTOffsetsAndRules(t *testing.T) {
	doc := "1\n00:00:01,000 --> 00:00:02,000\nhello\n\n" +
		"2\n00:00:03,000 --> 00:00:03,000\n\n" + // empty cue
		"3\n00:00:05,000 --> 00:00:04,000\ninverted\n"
	res, err := ParseSRT(strings.NewReader(doc), testRate(t), nil)
	if err != nil {
		t.Fatalf("parser returned hard error: %v", err)
	}
	if len(res.Cues) != 2 {
		t.Fatalf("inverted cue must be dropped, got %d cues", len(res.Cues))
	}
	if res.Cues[0].StartOffset != 0 {
		t.Errorf("first cue offset=%d want 0", res.Cues[0].StartOffset)
	}
	counts := res.Issues.CountByCode()
	if counts[model.CodeEmptyCue] != 1 {
		t.Errorf("empty cue count=%d", counts[model.CodeEmptyCue])
	}
	if counts[model.CodeInvertedCue] != 1 {
		t.Errorf("inverted cue count=%d", counts[model.CodeInvertedCue])
	}
	if !res.Issues.HasError() {
		t.Error("inverted cue must be an error-severity issue")
	}
}

func TestParseSRTOverlapAndCutPoint(t *testing.T) {
	doc := "1\n00:00:01,000 --> 00:00:03,000\na\n\n" +
		"2\n00:00:02,000 --> 00:00:04,000\nb\n"
	cuts := CutPoints{clock.MustRat(2, 1)}
	res, err := ParseSRT(strings.NewReader(doc), testRate(t), cuts)
	if err != nil {
		t.Fatal(err)
	}
	counts := res.Issues.CountByCode()
	if counts[model.CodeOverlappingCue] != 1 {
		t.Errorf("overlap count=%d", counts[model.CodeOverlappingCue])
	}
	if counts[model.CodeCueAtCutPoint] != 1 || !res.Cues[1].AtCutPoint {
		t.Errorf("cut-point detection failed: %v", counts)
	}
}

func TestParseSRTBadInput(t *testing.T) {
	if _, err := ParseSRT(strings.NewReader("x\n00:00:01,000 --> 00:00:02,000\n"),
		testRate(t), nil); err == nil {
		t.Error("non-numeric index must be a hard parse error")
	}
	if _, err := ParseSRT(strings.NewReader("1\nnot a timing line\n"),
		testRate(t), nil); err == nil {
		t.Error("malformed timing must be a hard parse error")
	}
}

func TestParseWebVTT(t *testing.T) {
	doc := "WEBVTT\n\n" +
		"intro\n00:00:01.000 --> 00:00:02.000\nfirst\n\n" +
		"00:00:02.500 --> 00:00:03.500 align:start\nsecond line\n\n" +
		"NOTE skip this block\nuntil blank line\n\n" +
		"00:00:05.000 --> 00:00:04.000\nbad\n"
	res, err := ParseWebVTT(strings.NewReader(doc), testRate(t), nil)
	if err != nil {
		t.Fatalf("hard error: %v", err)
	}
	if len(res.Cues) != 2 {
		t.Fatalf("want 2 valid cues, got %d", len(res.Cues))
	}
	if res.Cues[0].Text != "first" || res.Cues[1].Text != "second line" {
		t.Errorf("cue payloads wrong: %+v", res.Cues)
	}
	if res.Issues.CountByCode()[model.CodeInvertedCue] != 1 {
		t.Error("inverted VTT cue must be reported")
	}
}

func TestParseWebVTTHeaderAndCRLF(t *testing.T) {
	if _, err := ParseWebVTT(strings.NewReader("garbage\n"), testRate(t), nil); err == nil {
		t.Error("missing WEBVTT header must error")
	}
	doc := "WEBVTT\r\n\r\n00:00:01.000 --> 00:00:02.000\r\ncrlf cue\r\n"
	res, err := ParseWebVTT(strings.NewReader(doc), testRate(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Cues) != 1 || res.Cues[0].StartOffset != 10 {
		t.Errorf("crlf parsing wrong: %+v", res.Cues)
	}
}

func TestByteAccounting(t *testing.T) {
	doc := "1\n00:00:01,000 --> 00:00:02,000\nx\n"
	res, err := ParseSRT(strings.NewReader(doc), testRate(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Bytes != int64(len(doc)) {
		t.Errorf("bytes=%d want %d", res.Bytes, len(doc))
	}
}
