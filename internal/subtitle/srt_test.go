package subtitle

import (
	"strings"
	"testing"

	"archivecheck/internal/clock"
	"archivecheck/internal/diagnostic"
)

func TestParseSRTBoundariesPrecisionAndMissingFinalNewline(t *testing.T) {
	data := []byte("1\n00:00:00,500 --> 01:00:00,250\nHour boundary\n\n" +
		"2\n00:00:01,000 --> 00:00:02,000\nNo final newline")
	track, err := ParseSRT(11, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(track.Cues) != 2 {
		t.Fatalf("cue count = %d, want 2", len(track.Cues))
	}
	first := track.Cues[0]
	if first.Start != 500*clock.Millisecond {
		t.Fatalf("start = %d, want 500ms", first.Start)
	}
	if first.End != clock.Hour+250*clock.Millisecond {
		t.Fatalf("end = %d, want one hour +250ms", first.End)
	}
	if track.Cues[1].Text != "No final newline" {
		t.Fatalf("final text = %q", track.Cues[1].Text)
	}
}

func TestParseSRTEmptyTrack(t *testing.T) {
	for _, data := range [][]byte{nil, {}, []byte("\n\r\n")} {
		track, err := ParseSRT(2, data)
		if err != nil {
			t.Fatalf("ParseSRT(%q): %v", data, err)
		}
		if len(track.Cues) != 0 || len(track.Timeline.Intervals) != 0 {
			t.Fatalf("expected empty track, got %+v", track)
		}
	}
}

func TestParseSRTOverlapAndStableOrderAtSameTimestamp(t *testing.T) {
	data := []byte(strings.Join([]string{
		"3", "00:00:03,000 --> 00:00:04,000", "later", "",
		"1", "00:00:01,000 --> 00:00:03,500", "same-first", "",
		"2", "00:00:01,000 --> 00:00:01,500", "same-second", "",
	}, "\n"))
	track, err := ParseSRT(3, data)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{track.Cues[0].Text, track.Cues[1].Text, track.Cues[2].Text}
	want := []string{"same-first", "same-second", "later"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	if overlaps := track.Timeline.Overlaps(); len(overlaps) == 0 {
		t.Fatal("expected overlapping cues to be detectable")
	}
}

func TestParseSRTIllegalTimestampLocatesSample(t *testing.T) {
	data := []byte("1\n00:00:61,000 --> 00:00:02,000\nBad minute\n")
	_, err := ParseSRT(17, data)
	de, ok := diagnostic.From(err)
	if !ok || de.Code != diagnostic.CodeInvalidTimestamp {
		t.Fatalf("error = %v, want INVALID_TIMESTAMP", err)
	}
	if de.Sample != 17 || de.Phase != diagnostic.PhaseSubtitle || de.Offset != 2 {
		t.Fatalf("location = %+v, want subtitle/sample17/offset2", de)
	}
}

func TestParseSRTTruncatedTiming(t *testing.T) {
	data := []byte("1\n00:00:01,000 --> 00:00:0")
	_, err := ParseSRT(18, data)
	if !diagnostic.HasCode(err, diagnostic.CodeTruncated) {
		t.Fatalf("error = %v, want TRUNCATED_INPUT", err)
	}
	de, _ := diagnostic.From(err)
	if de.Sample != 18 || de.Offset != 2 {
		t.Fatalf("location = sample%d offset%d, want 18/2", de.Sample, de.Offset)
	}
}

func TestParseSRTOversizedLineOffset(t *testing.T) {
	data := []byte(strings.Repeat("x", 32))
	_, err := ParseSRTWithLimits(19, data, Limits{MaxLineBytes: 10, MaxInputBytes: 100, MaxCues: 10})
	if !diagnostic.HasCode(err, diagnostic.CodeLineTooLarge) {
		t.Fatalf("error = %v, want LINE_TOO_LARGE", err)
	}
	de, _ := diagnostic.From(err)
	if de.Sample != 19 || de.Offset != 10 {
		t.Fatalf("offset = %d, want 10", de.Offset)
	}
}
