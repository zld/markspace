package cli

import (
	"testing"

	"subx/internal/format"
	"subx/internal/timeline"
)

func TestParseFixture(t *testing.T) {
	o, err := Parse([]string{"fixture", "--dir", "/tmp/x"})
	if err != nil || o.Cmd != "fixture" || o.Dir != "/tmp/x" {
		t.Fatalf("o=%+v err=%v", o, err)
	}
	if _, err := Parse([]string{"fixture"}); err == nil {
		t.Fatal("missing --dir accepted")
	}
}

func TestParseConvert(t *testing.T) {
	o, err := Parse([]string{"convert", "--input", "a.srt", "--output", "b.vtt",
		"--fps", "24000/1001", "--overlap", "trim", "--encoding", "gbk", "--report", "r.json"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Batch.To != format.VTT || o.Batch.Encoding != "gbk" || o.Batch.Overlap != timeline.Trim {
		t.Fatalf("o=%+v", o)
	}
	if o.Batch.FPS.Num != 24000 || o.Batch.FPS.Den != 1001 {
		t.Fatalf("fps=%v", o.Batch.FPS)
	}
}

func TestParseConvertDefaults(t *testing.T) {
	o, err := Parse([]string{"convert", "--input", "a.srt", "--output", "b.srt"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Batch.FPS.Num != 24 || o.Batch.Overlap != timeline.Keep || o.Batch.To != format.SRT {
		t.Fatalf("defaults: %+v", o.Batch)
	}
}

func TestParseConvertErrors(t *testing.T) {
	for _, args := range [][]string{
		{"convert"},
		{"convert", "--input", "a.srt", "--output", "b.txt"},
		{"convert", "--input", "a.srt", "--output", "b.vtt", "--fps", "23.976"},
		{"convert", "--input", "a.srt", "--output", "b.vtt", "--overlap", "merge"},
		{"convert", "--input", "a.srt", "--output", "b.vtt", "--encoding", "latin1"},
		{"convert", "--input", "a.srt", "--output", "b.vtt", "--to", "ass"},
		{"nonsense"},
		{},
	} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("args %v accepted", args)
		} else if _, ok := err.(*UsageError); !ok {
			t.Fatalf("args %v: want *UsageError, got %T", args, err)
		}
	}
}

func TestParseBatch(t *testing.T) {
	o, err := Parse([]string{"batch", "--input-dir", "in", "--output-dir", "out",
		"--to", "vtt", "--jobs", "8", "--fps", "30"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Batch.Jobs != 8 || o.Batch.To != format.VTT || o.Batch.FPS.Num != 30 {
		t.Fatalf("o=%+v", o.Batch)
	}
	for _, args := range [][]string{
		{"batch", "--input-dir", "in"},
		{"batch", "--input-dir", "in", "--output-dir", "o", "--to", "vtt", "--jobs", "0"},
		{"batch", "--input-dir", "in", "--output-dir", "o", "--to", "vtt", "--jobs", "99"},
	} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("args %v accepted", args)
		}
	}
}
