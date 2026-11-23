package subtitle

import (
	"strings"
	"testing"

	"mediqc/internal/clock"
	"mediqc/internal/fault"
)

const goodSRT = `1
00:00:01,000 --> 00:00:03,500
Hello world

2
00:01:00,000 --> 00:01:02,000
Second cue
spans two lines
`

func TestParseSRTGood(t *testing.T) {
	tl, err := ParseSRT(strings.NewReader(goodSRT))
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Cues) != 2 {
		t.Fatalf("want 2 cues, got %d", len(tl.Cues))
	}
	if tl.Cues[0].Start != clock.Timescale || tl.Cues[0].End != 3*clock.Timescale+clock.Timescale/2 {
		t.Fatalf("cue 1 range wrong: %+v", tl.Cues[0])
	}
	if tl.Cues[1].Text != "Second cue\nspans two lines" {
		t.Fatalf("cue 2 text: %q", tl.Cues[1].Text)
	}
}

func TestSRTNegativeDuration(t *testing.T) {
	in := "1\n00:00:03,000 --> 00:00:01,000\nbackwards\n"
	_, err := ParseSRT(strings.NewReader(in))
	if !fault.IsCode(err, fault.CodeNegativeDuration) {
		t.Fatalf("want E_NEGATIVE_DURATION, got %v", err)
	}
	fe := err.(*fault.Error)
	if fe.Line != 2 {
		t.Fatalf("want line 2, got %d", fe.Line)
	}
}

func TestSRTTruncated(t *testing.T) {
	_, err := ParseSRT(strings.NewReader("1\n00:00:01,000 --> 00:00:02,000"))
	if !fault.IsCode(err, fault.CodeTruncatedInput) {
		t.Fatalf("want E_TRUNCATED_INPUT, got %v", err)
	}
	_, err = ParseSRT(strings.NewReader("3\n"))
	if !fault.IsCode(err, fault.CodeTruncatedInput) {
		t.Fatalf("want E_TRUNCATED_INPUT for missing timing, got %v", err)
	}
}

func TestSRTDayOverflow(t *testing.T) {
	in := "1\n23:59:59,000 --> 24:00:01,000\nover midnight\n"
	_, err := ParseSRT(strings.NewReader(in))
	if !fault.IsCode(err, fault.CodeDayOverflow) {
		t.Fatalf("want E_DAY_OVERFLOW, got %v", err)
	}
}

func TestSRTBadTimecode(t *testing.T) {
	in := "1\n00:61:00,000 --> 00:62:00,000\nbad minutes\n"
	_, err := ParseSRT(strings.NewReader(in))
	if !fault.IsCode(err, fault.CodeBadTimecode) {
		t.Fatalf("want E_BAD_TIMECODE, got %v", err)
	}
}
