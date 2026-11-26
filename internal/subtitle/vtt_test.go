package subtitle

import (
	"strings"
	"testing"

	"mediamerge/internal/fault"
)

func TestParseVTTBasic(t *testing.T) {
	in := "WEBVTT\n\n" +
		"NOTE this is a note\nspanning two lines\n\n" +
		"cue-1\n00:00:01.000 --> 00:00:02.000\nFirst\n\n" +
		"00:02.500 --> 00:04.000 align:start\nSecond\n"
	tr, err := ParseVTT("c.vtt", "v2", strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseVTT: %v", err)
	}
	if tr.Format != "vtt" || tr.Version != "v2" || len(tr.Cues) != 2 {
		t.Fatalf("unexpected track: %+v", tr)
	}
	if tr.Cues[0].Text != "First" || tr.Cues[0].ID != "c.vtt#1" {
		t.Fatalf("identified cue mismatch: %+v", tr.Cues[0])
	}
	if int64(tr.Cues[1].Start) != 225000 || tr.Cues[1].Text != "Second" {
		t.Fatalf("MM:SS timestamp / settings strip failed: %+v", tr.Cues[1])
	}
}

func TestParseVTTHeaderRequired(t *testing.T) {
	if _, err := ParseVTT("no.vtt", "v1", strings.NewReader("00:00:01.000 --> 00:00:02.000\nX\n")); err == nil {
		t.Fatal("missing WEBVTT header must be rejected")
	}
}

func TestParseVTTErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		kind fault.Kind
	}{
		{"truncated cue", "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n", fault.KindTruncated},
		{"reversed", "WEBVTT\n\n00:00:03.000 --> 00:00:01.000\nX\n", fault.KindReversed},
		{"bad utf8", "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n\xff\n", fault.KindEncoding},
		{"out of order", "WEBVTT\n\n00:00:05.000 --> 00:00:06.000\nA\n\n00:00:01.000 --> 00:00:02.000\nB\n",
			fault.KindReversed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseVTT("bad.vtt", "v1", strings.NewReader(tc.in))
			fe := fault.As(err)
			if fe == nil || fe.Kind != tc.kind || fe.Line == 0 {
				t.Fatalf("got %v, want kind=%s with line number", err, tc.kind)
			}
		})
	}
}
