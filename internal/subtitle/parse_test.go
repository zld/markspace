package subtitle

import (
	"strings"
	"testing"

	"mediaverify/internal/errs"
)

func TestParseSRTValid(t *testing.T) {
	src := "1\n00:00:01,000 --> 00:00:02,500\nhello\nworld\n\n" +
		"2\n00:00:03,000 --> 00:00:04,000\nsecond\n"
	cues, err := ParseSRT(strings.NewReader(src), DefaultConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cues) != 2 {
		t.Fatalf("want 2 cues, got %d", len(cues))
	}
	if cues[0].Start.Ns() != 1_000_000_000 || cues[0].End.Ns() != 2_500_000_000 {
		t.Fatalf("bad timing: %+v", cues[0])
	}
	if len(cues[0].Lines) != 2 || cues[0].Lines[1] != "world" {
		t.Fatalf("bad body: %+v", cues[0].Lines)
	}
	if cues[1].FirstRow != 6 {
		t.Fatalf("want first row 6, got %d", cues[1].FirstRow)
	}
}

func TestParseVTTValid(t *testing.T) {
	src := "WEBVTT\n\n" +
		"cue-1\n00:01.000 --> 00:02.500 align:start\nhello\n\n" +
		"NOTE skip this\nblock\n\n" +
		"00:03.000 --> 00:04.000\nsecond\n"
	cues, err := ParseWebVTT(strings.NewReader(src), DefaultConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cues) != 2 {
		t.Fatalf("want 2 cues, got %d", len(cues))
	}
	if cues[0].Start.Ns() != 1_000_000_000 {
		t.Fatalf("mm:ss timestamp not parsed: %d", cues[0].Start.Ns())
	}
}

func TestRejectNegativeDuration(t *testing.T) {
	src := "1\n00:00:03,000 --> 00:00:02,000\nbody\n"
	_, err := ParseSRT(strings.NewReader(src), DefaultConfig())
	if errs.Class(err) != errs.KindInvalidInput {
		t.Fatalf("want invalid_input, got %v", err)
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("error must report line number: %v", err)
	}
}

func TestRejectIllegalTimestamp(t *testing.T) {
	src := "1\n00:00:01,00 --> 00:00:02,000\nbody\n"
	if _, err := ParseSRT(strings.NewReader(src), DefaultConfig()); errs.Class(err) != errs.KindInvalidInput {
		t.Fatalf("want invalid_input for bad fraction, got %v", err)
	}
	src = "WEBVTT\n\n00:61.000 --> 00:62.000\nx\n"
	if _, err := ParseWebVTT(strings.NewReader(src), DefaultConfig()); errs.Class(err) != errs.KindInvalidInput {
		t.Fatalf("want invalid_input for minute 61, got %v", err)
	}
}

func TestRejectEmptyCue(t *testing.T) {
	src := "1\n00:00:01,000 --> 00:00:02,000\n\n2\n00:00:03,000 --> 00:00:04,000\nok\n"
	_, err := ParseSRT(strings.NewReader(src), DefaultConfig())
	if errs.Class(err) != errs.KindInvalidInput {
		t.Fatalf("want invalid_input for empty cue, got %v", err)
	}
}

func TestRejectOversizeCue(t *testing.T) {
	cfg := Config{MaxTextBytes: 10, MaxAssetChars: 1_000_000}
	long := strings.Repeat("x", 11)
	src := "1\n00:00:01,000 --> 00:00:02,000\n" + long + "\n"
	_, err := ParseSRT(strings.NewReader(src), cfg)
	if errs.Class(err) != errs.KindLimitExceeded {
		t.Fatalf("want limit_exceeded, got %v", err)
	}
}

func TestRejectTrackSizeLimit(t *testing.T) {
	cfg := Config{MaxTextBytes: 1000, MaxAssetChars: 20}
	src := "1\n00:00:01,000 --> 00:00:02,000\nabcdefghijklmnop\n\n" +
		"2\n00:00:03,000 --> 00:00:04,000\nqrstuvwxyz\n"
	_, err := ParseSRT(strings.NewReader(src), cfg)
	if errs.Class(err) != errs.KindLimitExceeded {
		t.Fatalf("want limit_exceeded for 1M-char budget, got %v", err)
	}
}

func TestRejectBadSequenceAndHeader(t *testing.T) {
	src := "2\n00:00:01,000 --> 00:00:02,000\nbody\n"
	if _, err := ParseSRT(strings.NewReader(src), DefaultConfig()); errs.Class(err) != errs.KindInvalidInput {
		t.Fatalf("want invalid_input for seq gap, got %v", err)
	}
	if _, err := ParseWebVTT(strings.NewReader("not a header\n"), DefaultConfig()); errs.Class(err) != errs.KindInvalidInput {
		t.Fatalf("want invalid_input for missing WEBVTT, got %v", err)
	}
}
