package subtitle

import (
	"strings"
	"testing"

	"mediamerge/internal/fault"
)

func TestParseSRTBasic(t *testing.T) {
	in := "1\r\n00:00:01,000 --> 00:00:02,500\r\nHello\r\n\r\n" +
		"2\n00:00:02,500 --> 00:00:04,000\nWorld line 1\nWorld line 2\n"
	tr, err := ParseSRT("clip.srt", "v1", strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseSRT: %v", err)
	}
	if tr.Format != "srt" || tr.Version != "v1" || len(tr.Cues) != 2 {
		t.Fatalf("unexpected track: %+v", tr)
	}
	c := tr.Cues[0]
	if int64(c.Start) != 90000 || int64(c.End) != 225000 || c.Text != "Hello" || c.Index != 1 ||
		c.ID != "clip.srt#1" || c.Source != "clip.srt" {
		t.Fatalf("cue 0 mismatch: %+v", c)
	}
	if tr.Cues[1].Text != "World line 1\nWorld line 2" {
		t.Fatalf("multi-line text not preserved: %q", tr.Cues[1].Text)
	}
}

func TestParseSRTBOM(t *testing.T) {
	in := string(append([]byte{0xEF, 0xBB, 0xBF},
		[]byte("1\n00:00:01,000 --> 00:00:02,000\nBOM ok\n")...))
	tr, err := ParseSRT("b.srt", "v1", strings.NewReader(in))
	if err != nil || len(tr.Cues) != 1 || tr.Cues[0].Text != "BOM ok" {
		t.Fatalf("BOM handling failed: %v %+v", err, tr)
	}
}

func TestParseSRTErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		kind fault.Kind
		line int
	}{
		{"bad utf8", "1\n00:00:01,000 --> 00:00:02,000\n\xff\xfe bad\n", fault.KindEncoding, 3},
		{"truncated block", "1\n00:00:01,000 --> 00:00:02,000\n", fault.KindTruncated, 1},
		{"missing arrow", "1\n00:00:01,000 - 00:00:02,000\nText\n", fault.KindTimecode, 2},
		{"reversed interval", "1\n00:00:03,000 --> 00:00:01,000\nText\n", fault.KindReversed, 2},
		{"out of order", "1\n00:00:05,000 --> 00:00:06,000\nA\n\n2\n00:00:01,000 --> 00:00:02,000\nB\n",
			fault.KindReversed, 6},
		{"bad index", "x\n00:00:01,000 --> 00:00:02,000\nText\n", fault.KindTimecode, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSRT("bad.srt", "v1", strings.NewReader(tc.in))
			fe := fault.As(err)
			if fe == nil {
				t.Fatal("expected error, got nil")
			}
			if fe.Kind != tc.kind || fe.Line != tc.line {
				t.Fatalf("got kind=%s line=%d, want kind=%s line=%d (%v)", fe.Kind, fe.Line, tc.kind, tc.line, err)
			}
		})
	}
}

func TestParseSRTTruncatedLastCue(t *testing.T) {
	// Final block has a timestamp but no text.
	in := "1\n00:00:01,000 --> 00:00:02,000\nGood\n\n2\n00:00:03,000 --> 00:00:04,000\n"
	_, err := ParseSRT("cut.srt", "v1", strings.NewReader(in))
	fe := fault.As(err)
	if fe == nil || fe.Kind != fault.KindTruncated || fe.Line != 5 {
		t.Fatalf("expected truncated error at line 5, got %v", err)
	}
}
