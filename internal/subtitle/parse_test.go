package subtitle

import (
	"errors"
	"strings"
	"testing"

	"regress/internal/media"
)

const sampleSRT = "1\n" +
	"00:00:01,000 --> 00:00:02,500\n" +
	"Hello world\n" +
	"\n" +
	"2\n" +
	"00:00:03,000 --> 00:00:04,000\n" +
	"Second cue\n"

const sampleVTT = "WEBVTT\n" +
	"\n" +
	"cue-1\n" +
	"00:00:01.000 --> 00:00:02.500\n" +
	"Hello world\n" +
	"\n" +
	"00:00:03.000 --> 00:00:04.000 line:80%\n" +
	"Second cue\n"

func TestParseSRT(t *testing.T) {
	doc, err := Parse(strings.NewReader(sampleSRT), "srt")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Cues) != 2 {
		t.Fatalf("cues=%d, want 2", len(doc.Cues))
	}
	c := doc.Cues[0]
	if c.ID != "1" || c.Start.String() != "1" || c.End.String() != "5/2" || c.Text != "Hello world" {
		t.Fatalf("first cue wrong: %+v", c)
	}
	if doc.PayloadDigest == "" || doc.ContainerDigest == "" ||
		doc.PayloadDigest == doc.ContainerDigest {
		t.Fatal("payload and container digests must both exist and differ")
	}
}

func TestParseVTT(t *testing.T) {
	doc, err := Parse(strings.NewReader(sampleVTT), "vtt")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Cues) != 2 {
		t.Fatalf("cues=%d, want 2", len(doc.Cues))
	}
	if doc.Cues[0].ID != "cue-1" || doc.Cues[0].Text != "Hello world" {
		t.Fatalf("first cue wrong: %+v", doc.Cues[0])
	}
	// Second block has no explicit id and carries cue settings.
	if doc.Cues[1].ID != "2" || doc.Cues[1].End.String() != "4" {
		t.Fatalf("second cue wrong: %+v", doc.Cues[1])
	}
}

func TestDigestsTrackContentNotPath(t *testing.T) {
	a, err := Parse(strings.NewReader(sampleSRT), "srt")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse(strings.NewReader(sampleSRT), "srt")
	if err != nil {
		t.Fatal(err)
	}
	if a.PayloadDigest != b.PayloadDigest || a.ContainerDigest != b.ContainerDigest {
		t.Fatal("identical input must produce identical digests")
	}
	// A single cue text change must move the payload digest only.
	modified := strings.Replace(sampleSRT, "Hello world", "Hello Earth", 1)
	c, err := Parse(strings.NewReader(modified), "srt")
	if err != nil {
		t.Fatal(err)
	}
	if c.PayloadDigest == a.PayloadDigest {
		t.Fatal("cue text change must change payload digest")
	}
}

func TestInvalidSubtitleInputs(t *testing.T) {
	cases := map[string]string{
		"bad timestamp":     "1\n00:00:61,000 --> 00:00:02,000\ntext\n\n",
		"end before start":  "1\n00:00:03,000 --> 00:00:02,000\ntext\n\n",
		"missing arrow":     "1\n00:00:01,000 -> 00:00:02,000\ntext\n\n",
		"empty text":        "1\n00:00:01,000 --> 00:00:02,000\n\n",
		"no cues":           "",
	}
	for name, in := range cases {
		if _, err := Parse(strings.NewReader(in), "srt"); err == nil {
			t.Fatalf("%s: expected error", name)
		} else if _, ok := media.AsError(err); !ok {
			t.Fatalf("%s: error %v is not a structured media.Error", name, err)
		}
	}
	if _, err := Parse(strings.NewReader("not webvtt\n"), "vtt"); err == nil {
		t.Fatal("VTT without WEBVTT header must fail")
	}
	if _, err := Parse(strings.NewReader(sampleSRT), "avi"); err == nil {
		t.Fatal("unknown format must fail")
	}
}

func TestOversizeRejected(t *testing.T) {
	big := strings.NewReader(strings.Repeat("x", int(media.MaxInputBytes+1)))
	_, err := Parse(big, "srt")
	var me *media.Error
	if !errors.As(err, &me) || me.Code != media.CodeOversize {
		t.Fatalf("err=%v, want CodeOversize", err)
	}
}

func TestCRLFNormalization(t *testing.T) {
	crlf := strings.ReplaceAll(sampleSRT, "\n", "\r\n")
	doc, err := Parse(strings.NewReader(crlf), "srt")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Cues) != 2 || doc.Cues[1].Text != "Second cue" {
		t.Fatalf("CRLF parse wrong: %+v", doc.Cues)
	}
}
