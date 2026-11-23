package subtitle

import (
	"strings"
	"testing"
)

func roundtrip(t *testing.T, format, in string) {
	t.Helper()
	tl1, err := Parse(format, strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	tl2, err := Parse(format, strings.NewReader(Serialize(tl1)))
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if len(tl1.Cues) != len(tl2.Cues) {
		t.Fatalf("cue count %d -> %d", len(tl1.Cues), len(tl2.Cues))
	}
	for i := range tl1.Cues {
		a, b := tl1.Cues[i], tl2.Cues[i]
		if a.Start != b.Start || a.End != b.End || a.Text != b.Text {
			t.Fatalf("cue %d changed: %+v -> %+v", i, a, b)
		}
	}
}

func TestSRTRoundtrip(t *testing.T) { roundtrip(t, "srt", goodSRT) }
func TestVTTRoundtrip(t *testing.T) { roundtrip(t, "vtt", goodVTT) }
