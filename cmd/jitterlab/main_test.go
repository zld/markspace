package main

import "testing"

func TestParseArgsValid(t *testing.T) {
	o, err := parseArgs([]string{
		"--transport", "both", "--seed", "424242",
		"--duration", "8s", "--frame", "20ms",
		"--curve", "100ms", "--format", "jsonl",
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.seed != 424242 || o.transport != "both" {
		t.Fatalf("opts %+v", o)
	}
}

func TestParseArgsRejects(t *testing.T) {
	cases := [][]string{
		{"--transport", "carrier-pigeon"},
		{"--format", "yaml"},
		{"--duration", "0s"},
		{"--frame", "-1ms"},
		{"--duration", "10ms", "--frame", "20ms"},
	}
	for _, args := range cases {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("%v: expected error", args)
		}
	}
}
