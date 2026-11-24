package main

import "testing"

func TestParseArgsValid(t *testing.T) {
	o, err := parseArgs([]string{
		"demo", "--codec", "av1", "--seed", "42",
		"--chunk-bytes", "4096", "--interrupt-after", "2", "--resume",
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.command != "demo" || o.codec != "av1" || o.seed != 42 ||
		o.chunkBytes != 4096 || o.interruptAfter != 2 || !o.resume {
		t.Fatalf("parsed options wrong: %+v", o)
	}
}

func TestParseArgsRejectsInvalid(t *testing.T) {
	cases := [][]string{
		{"demo", "--codec", "av1"},                                // missing seed
		{"demo", "--seed", "1"},                                   // missing codec
		{"demo", "--codec", "av1", "--seed", "1", "--bogus", "x"}, // unknown flag
		{"demo", "--codec", "av1", "--seed", "-1"},                // negative seed
		{"demo", "--codec", "av1", "--seed", "1", "--chunk-bytes", "0"},
		{"demo", "--codec", "av1", "--seed", "1", "positional"},
		{"demo", "--codec", "av1", "--seed", "1", "--interrupt-after"},
	}
	for i, args := range cases {
		if _, err := parseArgs(args); err == nil {
			t.Fatalf("case %d must fail: %v", i, args)
		}
	}
}

func TestParseArgsDefaults(t *testing.T) {
	o, err := parseArgs([]string{"verify", "--codec", "h264", "--seed", "9"})
	if err != nil {
		t.Fatal(err)
	}
	if o.chunkBytes != 4096 || o.scenario != "normal" || o.resume {
		t.Fatalf("defaults wrong: %+v", o)
	}
	if o.workdir == "" {
		t.Fatal("workdir must default to a temp path")
	}
}
