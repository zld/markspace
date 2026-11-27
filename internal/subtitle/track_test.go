package subtitle_test

import (
	"strings"
	"testing"

	"archivecheck/internal/fault"
	"archivecheck/internal/subtitle"
	"archivecheck/internal/timebase"
)

const goodSRT = "1\r\n00:00:01,000 --> 00:00:03,500\r\nFirst line\r\nSecond line\r\n\r\n" +
	"2\r\n00:00:04,000 --> 00:00:06,000\r\nThird\r\n"

func TestParseSRTMultiLine(t *testing.T) {
	tr := subtitle.Parse("good.srt", []byte(goodSRT))
	if tr.Format != subtitle.FormatSRT || tr.Bad() {
		t.Fatalf("unexpected track %+v", tr)
	}
	if len(tr.Cues) != 2 {
		t.Fatalf("got %d cues", len(tr.Cues))
	}
	c := tr.Cues[0]
	if c.Start != timebase.Nanos(1e9) || c.End != timebase.Nanos(3500*1e6) {
		t.Errorf("cue times = %d..%d", c.Start, c.End)
	}
	if c.Text() != "First line\nSecond line" || c.Index != 1 {
		t.Errorf("cue body = %q", c.Text())
	}
	if c.Offset == 0 {
		t.Error("cue should carry its byte offset")
	}
}

func TestParseSRTRecoversAfterBadCue(t *testing.T) {
	bad := "1\n00:00:01,000 --> 00:00:02,000\nok one\n\n" +
		"2\n00:00:03,000 --> 00:00:02,000\nbackwards\n\n" +
		"3\n00:00:0X,000 --> 00:00:05,000\nbad digit\n\n" +
		"4\n00:00:06,000 --> 00:00:07,000\nok two\n"
	tr := subtitle.Parse("mixed.srt", []byte(bad))
	if len(tr.Cues) != 2 {
		t.Fatalf("got %d recoverable cues, want 2: %+v", len(tr.Cues), tr.Cues)
	}
	if len(tr.Problems) != 2 {
		t.Fatalf("got %d problems, want 2", len(tr.Problems))
	}
	if tr.Cues[1].Text() != "ok two" {
		t.Errorf("scan did not resume: %+v", tr.Cues[1])
	}
	for _, p := range tr.Problems {
		if p.Offset <= 0 || p.Err == nil {
			t.Errorf("problem without offset: %+v", p)
		}
		if !fault.Is(p.Err, fault.StageSubtitle, "") && fault.As(p.Err) == nil {
			t.Errorf("problem without fault: %v", p.Err)
		}
	}
	if !tr.Bad() || tr.Err() == nil {
		t.Error("Err should be non-nil for a bad track")
	}
}

func TestParseSRTTruncatedBodyKeepsOffset(t *testing.T) {
	full := goodSRT + "\r\n3\r\n00:00:07,000 --> 00:00:09,000\r\ncut off here"
	cut := full[:len(full)-5]
	tr := subtitle.Parse("cut.srt", []byte(cut))
	// The final cue lost its terminating newline but remains parseable.
	if len(tr.Cues) != 3 {
		t.Fatalf("got %d cues", len(tr.Cues))
	}
	last := tr.Cues[2]
	if last.Text() != "cut off" {
		t.Errorf("truncated body = %q", last.Text())
	}
	if last.Offset < int64(len(goodSRT)) {
		t.Errorf("offset %d not beyond the intact prefix %d", last.Offset, len(goodSRT))
	}
}

func TestParseSRTStructuralDamage(t *testing.T) {
	data := "1\nnot a timing line\n\n2\n00:00:01,000 --> 00:00:02,000\nkept\n"
	tr := subtitle.Parse("struct.srt", []byte(data))
	if len(tr.Cues) != 1 || len(tr.Problems) != 1 {
		t.Fatalf("cues=%d problems=%d", len(tr.Cues), len(tr.Problems))
	}
	if got := tr.Problems[0].Offset; got != 2 {
		t.Errorf("problem offset = %d, want 2", got)
	}
}

const goodVTT = "WEBVTT - archive sample\nKind: captions\n\n" +
	"NOTE this is ignored\n\n" +
	"intro\n00:00:01.500 --> 00:00:03.000 line:0 position:50% align:middle\nHello\nworld\n\n" +
	"00:00:04.000 --> 00:00:05.000\nNo identifier\n"

func TestParseVTTIdentifiersSettingsAndMultiLine(t *testing.T) {
	tr := subtitle.Parse("good.vtt", []byte(goodVTT))
	if tr.Format != subtitle.FormatVTT || tr.Bad() {
		t.Fatalf("unexpected track %+v", tr)
	}
	if len(tr.Cues) != 2 {
		t.Fatalf("got %d cues", len(tr.Cues))
	}
	c := tr.Cues[0]
	if c.ID != "intro" || c.Start != timebase.Nanos(1500*1e6) {
		t.Errorf("cue = %+v", c)
	}
	if c.Settings != "line:0 position:50% align:middle" {
		t.Errorf("settings = %q", c.Settings)
	}
	if c.Text() != "Hello\nworld" {
		t.Errorf("body = %q", c.Text())
	}
	if tr.Cues[1].ID != "" || tr.Cues[1].Start != timebase.Nanos(4e9) {
		t.Errorf("second cue = %+v", tr.Cues[1])
	}
}

func TestParseVTTRecovery(t *testing.T) {
	data := "WEBVTT\n\n00:00:01.000 --> 00:00:0X.000\nbad\n\n" +
		"00:00:02.000 --> 00:00:03.000 sideways:1\nbad setting\n\n" +
		"00:00:04.000 --> 00:00:05.000\nkept\n"
	tr := subtitle.Parse("bad.vtt", []byte(data))
	if len(tr.Cues) != 1 || tr.Cues[0].Text() != "kept" {
		t.Fatalf("cues = %+v", tr.Cues)
	}
	if len(tr.Problems) != 2 {
		t.Fatalf("problems = %d", len(tr.Problems))
	}
	if tr.Problems[0].Offset >= tr.Problems[1].Offset {
		t.Error("problems should be ordered by offset")
	}
	if !strings.Contains(tr.Err().Error(), "subtitle") {
		t.Errorf("error lacks stage: %v", tr.Err())
	}
}

func TestParseVTTEmptyAndSignature(t *testing.T) {
	tr := subtitle.Parse("empty.vtt", []byte(""))
	if tr.Format != subtitle.FormatVTT && tr.Bad() {
		t.Logf("empty detected as %s", tr.Format)
	}
	tr = subtitle.Parse("bom.vtt", []byte("\xef\xbb\xbfWEBVTT\n\n00:01.000 --> 00:02.000\nm:ss\n"))
	if len(tr.Cues) != 1 || tr.Bad() {
		t.Fatalf("BOM/MS:SS cue not parsed: %+v", tr)
	}
	if tr.Cues[0].Start != timebase.Nanos(1e9) {
		t.Errorf("MM:SS start = %d", tr.Cues[0].Start)
	}
}
