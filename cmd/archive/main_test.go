package main

import (
	"testing"
)

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"invalid input", errInvalid(), 2},
		{"limit", errLimit(), 2},
		{"corrupt", errCorrupt(), 3},
		{"internal", errInternal(), 1},
		{"plain error", errPlain(), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeFor(tc.err); got != tc.want {
				t.Fatalf("exitCodeFor(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestDurationFlagParsing(t *testing.T) {
	cases := []struct {
		in      string
		seconds int
		ok      bool
	}{
		{"1800s", 1800, true},
		{"5m", 300, true},
		{"2h", 7200, true},
		{"500ms", 0, false}, // unsupported unit
		{"0s", 0, false},    // non-positive
		{"abc", 0, false},
	}
	for _, tc := range cases {
		d := &durationFlag{}
		err := d.Set(tc.in)
		if tc.ok {
			if err != nil || d.seconds != tc.seconds {
				t.Fatalf("Set(%q) = %d, %v; want %d", tc.in, d.seconds, err, tc.seconds)
			}
		} else if err == nil {
			t.Fatalf("Set(%q) expected error, got seconds=%d", tc.in, d.seconds)
		}
	}
}
