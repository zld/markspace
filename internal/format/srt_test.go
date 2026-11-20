package format

import (
	"strings"
	"testing"

	"subx/internal/cue"
)

const sampleSRT = `1
00:00:01,000 --> 00:00:03,000
第一句台词

00:00:03,500 --> 00:00:05,000
守卫：站住！
别动

3
00:00:05,000 --> 00:00:08,000
旁白
`

func TestParseSRT(t *testing.T) {
	cues, err := ParseSRT(sampleSRT, "in.srt")
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 3 {
		t.Fatalf("got %d cues", len(cues))
	}
	if cues[0].ID != "1" || cues[0].StartMs != 1000 || cues[0].EndMs != 3000 {
		t.Fatalf("cue0: %+v", cues[0])
	}
	if cues[1].ID != "" || len(cues[1].Lines) != 2 || cues[1].Lines[1] != "别动" {
		t.Fatalf("cue1: %+v", cues[1])
	}
	if cues[2].StartMs != 5000 || cues[2].EndMs != 8000 {
		t.Fatalf("cue2: %+v", cues[2])
	}
}

func TestParseSRTErrors(t *testing.T) {
	cases := []struct {
		name, text string
		wantLine   int
	}{
		{"bad timing", "1\nnot a timing\ntext\n", 2},
		{"empty text", "1\n00:00:01,000 --> 00:00:02,000\n", 2},
		{"reversed", "00:00:03,000 --> 00:00:02,000\ntext\n", 1},
		{"bad minutes", "00:60:00,000 --> 00:00:02,000\ntext\n", 1},
		{"empty file", "", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSRT(tc.text, "f.srt")
			pe, ok := err.(*ParseError)
			if !ok {
				t.Fatalf("want *ParseError, got %v", err)
			}
			if pe.Line != tc.wantLine || pe.File != "f.srt" {
				t.Fatalf("got %s:%d want line %d", pe.File, pe.Line, tc.wantLine)
			}
		})
	}
}

func TestSRTRoundTrip(t *testing.T) {
	cues, err := ParseSRT(sampleSRT, "in.srt")
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseSRT(string(SerializeSRT(cues)), "out.srt")
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != len(cues) {
		t.Fatalf("count %d != %d", len(back), len(cues))
	}
	for i := range cues {
		if back[i].StartMs != cues[i].StartMs || back[i].EndMs != cues[i].EndMs {
			t.Fatalf("cue %d times differ", i)
		}
		if strings.Join(back[i].Lines, "\n") != strings.Join(cues[i].Lines, "\n") {
			t.Fatalf("cue %d text differs", i)
		}
	}
	// serialized output renumbers sequentially
	if back[1].ID != "2" {
		t.Fatalf("serialized ID = %q", back[1].ID)
	}
}

func TestSerializeSRTFormat(t *testing.T) {
	out := string(SerializeSRT([]cue.Cue{{StartMs: 61001, EndMs: 62002, Lines: []string{"x"}}}))
	want := "1\n00:01:01,001 --> 00:01:02,002\nx\n\n"
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}
