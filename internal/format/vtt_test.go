package format

import (
	"strings"
	"testing"

	"subx/internal/cue"
)

const sampleVTT = `WEBVTT - demo file

NOTE this is a comment
spanning two lines

intro
00:01.000 --> 00:03.000
第一句台词

00:00:03.500 --> 00:00:05.000 line:90% position:50%
守卫：站住！
别动
`

func TestParseVTT(t *testing.T) {
	cues, err := ParseVTT(sampleVTT, "in.vtt")
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 2 {
		t.Fatalf("got %d cues", len(cues))
	}
	if cues[0].ID != "intro" || cues[0].StartMs != 1000 || cues[0].EndMs != 3000 {
		t.Fatalf("cue0: %+v", cues[0])
	}
	if cues[1].Settings != "line:90% position:50%" || len(cues[1].Lines) != 2 {
		t.Fatalf("cue1: %+v", cues[1])
	}
}

func TestParseVTTErrors(t *testing.T) {
	cases := []struct {
		name, text string
		wantLine   int
	}{
		{"no header", "1\n00:00:01.000 --> 00:00:02.000\nx\n", 1},
		{"bad header", "WEBVTTX\n\n00:00:01.000 --> 00:00:02.000\nx\n", 1},
		{"bad timing", "WEBVTT\n\nid\nnonsense\nx\n", 4},
		{"no cues", "WEBVTT\n", 1},
		{"empty text", "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseVTT(tc.text, "f.vtt")
			pe, ok := err.(*ParseError)
			if !ok {
				t.Fatalf("want *ParseError, got %v", err)
			}
			if pe.Line != tc.wantLine {
				t.Fatalf("got line %d want %d (%v)", pe.Line, tc.wantLine, pe)
			}
		})
	}
}

func TestVTTRoundTrip(t *testing.T) {
	cues, err := ParseVTT(sampleVTT, "in.vtt")
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseVTT(string(SerializeVTT(cues)), "out.vtt")
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != len(cues) {
		t.Fatalf("count %d != %d", len(back), len(cues))
	}
	for i := range cues {
		a, b := cues[i], back[i]
		if a.ID != b.ID || a.StartMs != b.StartMs || a.EndMs != b.EndMs || a.Settings != b.Settings {
			t.Fatalf("cue %d differs: %+v vs %+v", i, a, b)
		}
		if strings.Join(a.Lines, "\n") != strings.Join(b.Lines, "\n") {
			t.Fatalf("cue %d text differs", i)
		}
	}
}

func TestSerializeVTTFormat(t *testing.T) {
	out := string(SerializeVTT([]cue.Cue{{ID: "c1", StartMs: 61001, EndMs: 62002, Settings: "align:center", Lines: []string{"x"}}}))
	want := "WEBVTT\n\nc1\n00:01:01.001 --> 00:01:02.002 align:center\nx\n\n"
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}
