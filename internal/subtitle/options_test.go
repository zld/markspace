package subtitle

import (
	"strings"
	"testing"

	"mediamerge/internal/clock"
	"mediamerge/internal/fault"
)

func TestLenientCollectsTruncatedCue(t *testing.T) {
	in := "1\n00:00:01,000 --> 00:00:02,000\nGood\n\n" +
		"2\n00:00:03,000 --> 00:00:04,000\n"
	tr, rej, err := ParseSRTWithOptions("d.srt", "v1", strings.NewReader(in),
		Options{Lenient: true, Rate: clock.Rate25})
	if err != nil {
		t.Fatalf("lenient parse must not abort: %v", err)
	}
	if len(tr.Cues) != 1 || tr.Cues[0].Text != "Good" {
		t.Fatalf("good cue must survive: %+v", tr.Cues)
	}
	if len(rej) != 1 || rej[0].Kind != fault.KindTruncated || rej[0].Line == 0 {
		t.Fatalf("truncated cue must be recorded with line: %+v", rej)
	}
}

func TestStrictStillAborts(t *testing.T) {
	in := "1\n00:00:01,000 --> 00:00:02,000\n"
	if _, _, err := ParseSRTWithOptions("d.srt", "v1", strings.NewReader(in),
		Options{Lenient: false, Rate: clock.Rate25}); err == nil {
		t.Fatal("strict parse must abort on a truncated cue")
	}
}

func TestDropFrameLabelRejectedInSubtitle(t *testing.T) {
	// Semicolon DF punctuation is illegal against the 25fps subtitle rate.
	in := "1\n00:00:01;00 --> 00:00:02;00\nX\n"
	_, _, err := ParseSRTWithOptions("d.srt", "v1", strings.NewReader(in),
		Options{Lenient: false, Rate: clock.Rate25})
	fe := fault.As(err)
	if fe == nil || (fe.Kind != fault.KindDropFrame && fe.Kind != fault.KindTimecode) {
		t.Fatalf("illegal DF label must be a drop-frame fault, got %v", err)
	}
	// Lenient mode captures it instead of aborting.
	_, rej, lerr := ParseSRTWithOptions("d.srt", "v1", strings.NewReader(in),
		Options{Lenient: true, Rate: clock.Rate25})
	if lerr != nil || len(rej) != 1 {
		t.Fatalf("lenient DF handling: err=%v rej=%+v", lerr, rej)
	}
}
