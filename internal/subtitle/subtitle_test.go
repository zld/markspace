package subtitle

import (
	"errors"
	"strings"
	"testing"

	"mediapack/internal/clock"
	"mediapack/internal/fault"
)

const sampleSRT = `1
00:00:01,000 --> 00:00:03,500
Hello crossing

2
00:00:04,000 --> 00:00:05,000
Second cue
`

func TestParseSRTKeepsDigestAndBoundaries(t *testing.T) {
	doc, err := ParseSRT("sample.srt", []byte(sampleSRT))
	if err != nil {
		t.Fatal(err)
	}
	if doc.RawSHA256 != Digest([]byte(sampleSRT)) || len(doc.RawSHA256) != 64 {
		t.Fatalf("bad raw digest %q", doc.RawSHA256)
	}
	if len(doc.Cues) != 2 {
		t.Fatalf("cues=%d want 2", len(doc.Cues))
	}
	if doc.Cues[0].Start != clock.Ticks(1800000) || doc.Cues[0].End != clock.Ticks(6300000) {
		t.Fatalf("boundaries=%d..%d", doc.Cues[0].Start, doc.Cues[0].End)
	}
	if !strings.Contains(doc.Cues[0].Text, "Hello crossing") {
		t.Fatalf("text=%q", doc.Cues[0].Text)
	}
}

func TestParseVTTAndRoundTrip(t *testing.T) {
	vtt := "WEBVTT\n\n00:00:01.500 --> 00:00:02.250\nVTT text\n"
	doc, err := ParseVTT("sample.vtt", []byte(vtt))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Cues) != 1 || doc.Cues[0].Start.Milliseconds() != 1500 {
		t.Fatalf("cues=%+v", doc.Cues)
	}
	again, err := ParseVTT("roundtrip.vtt", doc.ToVTT())
	if err != nil {
		t.Fatal(err)
	}
	if again.Cues[0].Text != "VTT text" || again.Cues[0].Start != doc.Cues[0].Start {
		t.Fatalf("roundtrip mismatch: %+v", again.Cues[0])
	}
}

func TestCueSplitAtSegmentBoundaryMergesExactly(t *testing.T) {
	doc, err := ParseSRT("sample.srt", []byte(sampleSRT))
	if err != nil {
		t.Fatal(err)
	}
	cue := doc.Cues[0]
	cuts := []clock.Ticks{clock.Ticks(2_000_000), clock.Ticks(3_000_000)}
	parts := SplitCueAt(cue, cuts)
	if len(parts) != 3 {
		t.Fatalf("parts=%d want 3", len(parts))
	}
	if parts[0].Start != cue.Start || parts[len(parts)-1].End != cue.End {
		t.Fatal("split must preserve outer boundaries")
	}
	for i := 1; i < len(parts); i++ {
		if parts[i].Start != parts[i-1].End {
			t.Fatal("split intervals are not half-open contiguous")
		}
	}
	merged, err := MergeFragments("merge", parts)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Start != cue.Start || merged.End != cue.End || merged.Text != cue.Text {
		t.Fatalf("merge=%+v cue=%+v", merged, cue)
	}
}

func TestOverlapAndZeroDurationAreClassified(t *testing.T) {
	zero := `1
00:00:01,000 --> 00:00:01,000
zero
`
	_, err := ParseSRT("zero.srt", []byte(zero))
	if fault.Classify(err) != fault.ZeroDurationCue {
		t.Fatalf("zero kind=%q err=%v", fault.Classify(err), err)
	}

	overlap := []Cue{
		{Index: 1, Start: 0, End: 100, Text: "a"},
		{Index: 2, Start: 90, End: 120, Text: "b"},
	}
	err = ValidateCues("overlap.srt", overlap)
	if fault.Classify(err) != fault.OverlappingCue {
		t.Fatalf("overlap kind=%q err=%v", fault.Classify(err), err)
	}
}

func TestMalformedTimestampHasSourceLocation(t *testing.T) {
	_, err := ParseSRT("bad.srt", []byte("1\n00:00:xx --> 00:00:01,000\nx\n"))
	var fe *fault.Error
	if fault.Classify(err) != fault.SubtitleParse || !errorAs(err, &fe) {
		t.Fatalf("expected located subtitle parse error: %v", err)
	}
	if fe.Source != "bad.srt" || fe.Offset <= 0 {
		t.Fatalf("bad location %+v", fe)
	}
}

func TestCRLFTimestampOffsetUsesOriginalBytes(t *testing.T) {
	raw := "1\r\n00:00:xx --> 00:00:01,000\r\nx\r\n"
	_, err := ParseSRT("crlf.srt", []byte(raw))
	var fe *fault.Error
	if !errors.As(err, &fe) {
		t.Fatalf("expected fault error: %v", err)
	}
	if fe.Offset != 3 {
		t.Fatalf("CRLF offset=%d want 3", fe.Offset)
	}
}

func errorAs(err error, target **fault.Error) bool {
	for err != nil {
		if e, ok := err.(*fault.Error); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
