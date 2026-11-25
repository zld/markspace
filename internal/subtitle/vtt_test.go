package subtitle

import (
	"testing"

	"archivecheck/internal/clock"
	"archivecheck/internal/diagnostic"
)

func TestParseVTTShortTimestampAndHeader(t *testing.T) {
	data := []byte("WEBVTT - demo\n\n00:01.500 --> 01:02:03.250 line:90%\n" +
		"WebVTT text\nwith two lines\n")
	track, err := ParseVTT(21, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(track.Cues) != 1 {
		t.Fatalf("cues = %d, want 1", len(track.Cues))
	}
	cue := track.Cues[0]
	if cue.Start != clock.Second+500*clock.Millisecond {
		t.Fatalf("start = %d", cue.Start)
	}
	wantEnd := clock.Hour + 2*clock.Minute + 3*clock.Second + 250*clock.Millisecond
	if cue.End != wantEnd {
		t.Fatalf("end = %d, want %d", cue.End, wantEnd)
	}
	if cue.Text != "WebVTT text\nwith two lines" {
		t.Fatalf("text = %q", cue.Text)
	}
}

func TestParseVTTEmptyTrackAndNoFinalNewline(t *testing.T) {
	track, err := ParseVTT(22, []byte("WEBVTT\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(track.Cues) != 0 {
		t.Fatalf("cues = %d, want empty", len(track.Cues))
	}

	track, err = ParseVTT(23, []byte("WEBVTT\n\ncue-id\n00:01.000 --> 00:02.000\nEOF"))
	if err != nil {
		t.Fatal(err)
	}
	if len(track.Cues) != 1 || track.Cues[0].ID != "cue-id" || track.Cues[0].Text != "EOF" {
		t.Fatalf("unexpected final cue: %+v", track.Cues)
	}
}

func TestParseVTTNoteAndStableOrder(t *testing.T) {
	data := []byte("WEBVTT\n\nNOTE translator note\nmultiple lines\n\n" +
		"a\n00:02.000 --> 00:03.000\nsecond-time\n\n" +
		"b\n00:01.000 --> 00:02.000\nfirst\n\n" +
		"c\n00:01.000 --> 00:01.500\nsame-start-later\n")
	track, err := ParseVTT(24, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(track.Cues) != 3 {
		t.Fatalf("cues = %d, want 3 (NOTE skipped)", len(track.Cues))
	}
	if track.Cues[0].Text != "first" || track.Cues[1].Text != "same-start-later" {
		t.Fatalf("stable order wrong: %q, %q", track.Cues[0].Text, track.Cues[1].Text)
	}
}

func TestParseVTTRejectsMissingSignatureAndBadTime(t *testing.T) {
	_, err := ParseVTT(25, []byte("not vtt\n"))
	if !diagnostic.HasCode(err, diagnostic.CodeInvalidSubtitle) {
		t.Fatalf("signature error = %v", err)
	}

	_, err = ParseVTT(26, []byte("WEBVTT\n\n00:70.000 --> 00:01.000\nbad\n"))
	if !diagnostic.HasCode(err, diagnostic.CodeInvalidTimestamp) {
		t.Fatalf("timestamp error = %v", err)
	}
	if de, _ := diagnostic.From(err); de.Sample != 26 {
		t.Fatalf("sample = %d, want 26", de.Sample)
	}
}
