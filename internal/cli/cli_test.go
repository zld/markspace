package cli

import (
	"bytes"
	"errors"
	"testing"
)

func TestParseValid(t *testing.T) {
	cases := map[string][]string{
		"demo":   {"demo", "--workspace", "/tmp/w", "--segments", "8", "--target-kbps", "600", "--workers", "3", "--fail-after", "3"},
		"run":    {"run", "--workspace", "/tmp/w", "--input", "/tmp/in.y4m"},
		"resume": {"resume", "--workspace", "/tmp/w"},
		"report": {"report", "--workspace", "/tmp/w"},
	}
	for name, args := range cases {
		o, err := Parse(args, &bytes.Buffer{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if o.Command != name {
			t.Fatalf("%s: command=%q", name, o.Command)
		}
	}
}

func TestParseErrors(t *testing.T) {
	bad := [][]string{
		{},
		{"bogus", "--workspace", "/tmp/w"},
		{"demo"}, // no workspace
		{"demo", "--workspace", "/tmp/w", "--workers", "9"},
		{"demo", "--workspace", "/tmp/w", "--workers", "0"},
		{"demo", "--workspace", "/tmp/w", "--target-kbps", "0"},
		{"demo", "--workspace", "/tmp/w", "--segments", "0"},
		{"run", "--workspace", "/tmp/w"}, // missing input
		{"resume", "--workspace", "/tmp/w", "--input", "/tmp/x"},
		{"demo", "--workspace", "/tmp/w", "--frames-per-segment", "-1"},
	}
	for _, args := range bad {
		var buf bytes.Buffer
		if _, err := Parse(args, &buf); !errors.Is(err, ErrUsage) {
			t.Errorf("%v: want ErrUsage, got %v", args, err)
		}
	}
}

func TestDefaults(t *testing.T) {
	o, err := Parse([]string{"demo", "--workspace", "/tmp/w"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if o.Workers != 1 || o.FramesPerSegment != 120 || o.Segments != 4 || o.TargetKbps != 600 {
		t.Fatalf("unexpected defaults: %+v", o)
	}
}
