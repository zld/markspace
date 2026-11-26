package subtitle

import (
	"testing"

	"mediasync/internal/clock"
	"mediasync/internal/issue"
)

const goodSRT = `1
00:00:01,000 --> 00:00:02,500
Hello

2
00:00:03,000 --> 00:00:04,000
World
second line
`

func TestParseSRTValid(t *testing.T) {
	tr, issues := ParseSRT("a.srt", goodSRT)
	if len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	if len(tr.Cues) != 2 {
		t.Fatalf("cues=%d", len(tr.Cues))
	}
	c := tr.Cues[0]
	if c.Start != clock.FromMilliseconds(1000) || c.End != clock.FromMilliseconds(2500) {
		t.Fatalf("times %s %s", c.Start, c.End)
	}
	if c.StartLn != 1 || c.EndLn != 2 {
		t.Fatalf("lines %d %d", c.StartLn, c.EndLn)
	}
	if tr.Cues[1].Text != "World\nsecond line" {
		t.Fatalf("text=%q", tr.Cues[1].Text)
	}
}

func TestParseSRTInvalidTimeHasLine(t *testing.T) {
	src := "1\n00:00:01,000 --> 00:00:0X,500\nBad\n\n2\n00:00:03,000 --> 00:00:04,000\nOK\n"
	tr, issues := ParseSRT("a.srt", src)
	if len(tr.Cues) != 1 {
		t.Fatalf("cues=%d", len(tr.Cues))
	}
	if len(issues) != 1 || issues[0].Category != issue.InvalidTime {
		t.Fatalf("issues=%v", issues)
	}
	if issues[0].Loc.Line != 2 {
		t.Fatalf("line=%d", issues[0].Loc.Line)
	}
}

func TestParseSRTNegativeDuration(t *testing.T) {
	src := "1\n00:00:05,000 --> 00:00:04,000\nBack\n"
	_, issues := ParseSRT("a.srt", src)
	if len(issues) != 1 || issues[0].Category != issue.NegativeDuration {
		t.Fatalf("issues=%v", issues)
	}
	if issues[0].Loc.Line != 2 {
		t.Fatalf("line=%d", issues[0].Loc.Line)
	}
}

func TestParseSRTOverlapReportsSourceLine(t *testing.T) {
	src := "1\n00:00:01,000 --> 00:00:05,000\nA\n\n2\n00:00:04,000 --> 00:00:06,000\nB\n"
	tr, issues := ParseSRT("a.srt", src)
	if len(tr.Cues) != 2 {
		t.Fatalf("cues=%d", len(tr.Cues))
	}
	if len(issues) != 1 || issues[0].Category != issue.OverlappingCue {
		t.Fatalf("issues=%v", issues)
	}
	if issues[0].Loc.Line != 5 {
		t.Fatalf("line=%d want 5", issues[0].Loc.Line)
	}
}

func TestParseSRTMalformedIndex(t *testing.T) {
	src := "x\n00:00:01,000 --> 00:00:02,000\nA\n\n5\n00:00:03,000 --> 00:00:04,000\nB\n"
	tr, issues := ParseSRT("a.srt", src)
	// SRT sequence numbers are optional, so the timing after the stray line
	// is still a valid cue; only the stray line itself is reported.
	if len(tr.Cues) != 2 || tr.Cues[1].Index != 5 {
		t.Fatalf("cues=%v", tr.Cues)
	}
	if len(issues) != 1 || issues[0].Category != issue.MalformedSubtitle || issues[0].Loc.Line != 1 {
		t.Fatalf("issues=%v", issues)
	}
}

const goodVTT = `WEBVTT

alpha
00:00:01.000 --> 00:00:02.500 align:start
Hello

00:03.000 --> 00:04.000
World
`

func TestParseVTTValid(t *testing.T) {
	tr, issues := ParseVTT("a.vtt", goodVTT)
	if len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	if len(tr.Cues) != 2 {
		t.Fatalf("cues=%d", len(tr.Cues))
	}
	if tr.Cues[0].Start != clock.FromMilliseconds(1000) || tr.Cues[0].End != clock.FromMilliseconds(2500) {
		t.Fatalf("times %s %s", tr.Cues[0].Start, tr.Cues[0].End)
	}
	// MM:SS form is normalized to the same absolute tick.
	if tr.Cues[1].Start != clock.FromMilliseconds(3000) {
		t.Fatalf("mm:ss start=%s", tr.Cues[1].Start)
	}
	if tr.Cues[0].StartLn != 3 || tr.Cues[0].EndLn != 4 {
		t.Fatalf("lines %d %d", tr.Cues[0].StartLn, tr.Cues[0].EndLn)
	}
}

func TestParseVTTInvalidAndOverlap(t *testing.T) {
	src := "WEBVTT\n\n00:00:01.000 --> 00:00:05.000\nA\n\n00:04.000 --> 00:00:06.000\nB\n\n00:00:07.000 --> 00:00:9.000\nC\n"
	tr, issues := ParseVTT("a.vtt", src)
	if len(tr.Cues) != 2 {
		t.Fatalf("cues=%d", len(tr.Cues))
	}
	cats := issues.Counts()
	if cats[issue.OverlappingCue] != 1 || cats[issue.InvalidTime] != 1 {
		t.Fatalf("issues=%v", issues)
	}
	for _, it := range issues {
		if it.Category == issue.InvalidTime && it.Loc.Line != 9 {
			t.Fatalf("invalid time line=%d", it.Loc.Line)
		}
	}
}

func TestParseVTTMissingHeader(t *testing.T) {
	_, issues := ParseVTT("a.vtt", "00:00:01.000 --> 00:00:02.000\nA\n")
	if len(issues) == 0 || issues[0].Category != issue.MalformedSubtitle || issues[0].Loc.Line != 1 {
		t.Fatalf("issues=%v", issues)
	}
}

func TestParseVTTNoteSkipped(t *testing.T) {
	src := "WEBVTT\n\nNOTE this is a comment\nspanning lines\n\n00:00:01.000 --> 00:00:02.000\nA\n"
	tr, issues := ParseVTT("a.vtt", src)
	if len(issues) != 0 || len(tr.Cues) != 1 {
		t.Fatalf("cues=%d issues=%v", len(tr.Cues), issues)
	}
}
