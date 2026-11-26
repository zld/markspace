package main

import (
	"path/filepath"
	"testing"
)

func TestValidateFlags(t *testing.T) {
	good := flags{
		generate: true, profile: "clean", seed: 7,
		workers: 2, queue: 4, lossBudget: 2,
		keyframeBudget: 3, windowMS: 100,
	}
	if err := validate(good); err != nil {
		t.Fatalf("valid flags rejected: %v", err)
	}
	bad := []flags{
		{generate: false, profile: "clean"},                  // no generate
		{generate: true, profile: "bogus"},                   // bad profile
		{generate: true, profile: "clean", workers: 0},       // bad workers
		{generate: true, profile: "clean", queue: 0},         // bad queue
		{generate: true, profile: "clean", keyframeBudget: 0}, // bad budget
		{generate: true, profile: "cancel", cancelAfter: 0},  // cancel needs N
	}
	for i, f := range bad {
		f.seed = 1
		f.windowMS = 100
		if err := validate(f); err == nil {
			t.Fatalf("case %d must be invalid: %+v", i, f)
		}
	}
}

func TestRunExitCodes(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"clean", []string{"--generate", "--profile", "clean", "--seed", "7",
			"--out", filepath.Join(dir, "clean.json")}, 0},
		{"damaged", []string{
			"--generate", "--profile", "damaged", "--seed", "7",
			"--workers", "4", "--queue", "8", "--loss-budget", "2",
			"--keyframe-budget", "3", "--out", filepath.Join(dir, "damaged.json"),
		}, 2},
		{"cancel", []string{
			"--generate", "--profile", "cancel", "--seed", "7",
			"--cancel-after", "5", "--workers", "2", "--queue", "2",
			"--out", filepath.Join(dir, "cancel.json"),
		}, 130},
		{"bad profile", []string{"--generate", "--profile", "nope"}, 3},
		{"bad flag", []string{"--workers", "0"}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(tc.args); got != tc.want {
				t.Fatalf("exit = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestPassFailAndSummaryText(t *testing.T) {
	if passFail(true) != "pass" || passFail(false) != "FAIL" {
		t.Fatal("passFail rendering wrong")
	}
}
