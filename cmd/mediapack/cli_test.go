package main

import "testing"

func TestParseDemoArgsValidation(t *testing.T) {
	cases := [][]string{
		{"--tracks", "0", "--work", t.TempDir()},
		{"--tracks", "-3", "--work", t.TempDir()},
		{"--ladders", "x", "--work", t.TempDir()},
		{"--segment", "0s", "--work", t.TempDir()},
		{"--faults", "maybe", "--work", t.TempDir()},
		{"--bogus"},
	}
	for _, c := range cases {
		if _, err := parseDemoArgs(c); err == nil {
			t.Fatalf("args %v should be rejected", c)
		}
	}
}

func TestRunDemoExitCodes(t *testing.T) {
	if code := runDemo([]string{"--tracks", "0"}); code != exitUsage {
		t.Fatalf("bad args code = %d, want %d", code, exitUsage)
	}
	dir := t.TempDir()
	code := runDemo([]string{
		"--work", dir, "--tracks", "2", "--ladders", "2",
		"--segment", "2s", "--faults", "all", "--resume",
	})
	if code != exitOK {
		t.Fatalf("demo code = %d, want %d", code, exitOK)
	}
}

func TestRunCheckExitCodes(t *testing.T) {
	if code := runCheck([]string{}); code != exitUsage {
		t.Fatalf("missing --work code = %d, want %d", code, exitUsage)
	}
	clean := t.TempDir()
	if code := runCheck([]string{"--work", clean}); code != exitOK {
		t.Fatalf("clean check code = %d, want %d", code, exitOK)
	}
	for _, mode := range []string{"--truncate-box", "--truncate-mxf", "--unalignable"} {
		dir := t.TempDir()
		if code := runCheck([]string{"--work", dir, mode}); code != exitDataFault {
			t.Fatalf("mode %s code = %d, want %d", mode, code, exitDataFault)
		}
	}
}
