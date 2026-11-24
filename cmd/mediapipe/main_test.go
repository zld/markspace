package main

import "testing"

func TestDemoHealthyExitZero(t *testing.T) {
	dir := t.TempDir()
	code := runDemo([]string{
		"--seed", "17", "--frames", "60", "--audio-rate", "44100",
		"--channels", "6", "--work-dir", dir,
	})
	if code != 0 {
		t.Fatalf("healthy demo exit = %d, want 0", code)
	}
}

func TestDemoInterruptResume(t *testing.T) {
	dir := t.TempDir()
	args := []string{
		"--seed", "17", "--frames", "60", "--audio-rate", "48000",
		"--channels", "2", "--interrupt-after", "2", "--resume",
		"--work-dir", dir,
	}
	if code := runDemo(args); code != 0 {
		t.Fatalf("interrupt+resume exit = %d, want 0", code)
	}
	// A second fully-cached run over the same directory must still succeed.
	if code := runDemo(append(args, "--interrupt-after", "0")); code != 0 {
		t.Fatalf("cached rerun exit = %d, want 0", code)
	}
}

func TestDemoCorruptionExitTwo(t *testing.T) {
	for _, kind := range []string{"truncated", "hash_mismatch", "unknown_color"} {
		dir := t.TempDir()
		code := runDemo([]string{
			"--seed", "17", "--frames", "60", "--audio-rate", "48000",
			"--channels", "2", "--corrupt", kind, "--work-dir", dir,
		})
		if code != 2 {
			t.Errorf("%s: exit = %d, want 2", kind, code)
		}
	}
}

func TestDemoInvalidParamsExitOne(t *testing.T) {
	cases := [][]string{
		{"--channels", "8"},
		{"--frames", "0"},
		{"--audio-rate", "22050"},
		{"--corrupt", "nope"},
	}
	for _, c := range cases {
		args := append([]string{"--work-dir", t.TempDir()}, c...)
		if code := runDemo(args); code != 1 {
			t.Errorf("%v: exit = %d, want 1", c, code)
		}
	}
}
