package subtitle_test

import (
	"testing"

	"archivecheck/internal/subtitle"
)

func TestTruncatedTimingLine(t *testing.T) {
	data := "1\n00:00:01,000 --> 00:00:02,000\nok\n\n2\n00:00:03,000 --> 00:00:"
	tr := subtitle.Parse("cut.srt", []byte(data))
	t.Logf("cues=%d problems=%d", len(tr.Cues), len(tr.Problems))
	for _, p := range tr.Problems {
		t.Logf("problem off=%d %v", p.Offset, p.Err)
	}
	if len(tr.Problems) == 0 {
		t.Fatal("truncated timing line was silently accepted")
	}
	if tr.Problems[0].Offset != int64(len("1\n00:00:01,000 --> 00:00:02,000\nok\n\n2\n")) {
		t.Errorf("offset = %d", tr.Problems[0].Offset)
	}
}
