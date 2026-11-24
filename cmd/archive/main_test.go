package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseList(t *testing.T) {
	got, err := parseList("900, 420,1100")
	if err != nil || len(got) != 3 || got[2] != 1100 {
		t.Fatalf("got %v err %v", got, err)
	}
	for _, bad := range []string{"", "x", "0", "900,,"} {
		if _, err := parseList(bad); err == nil {
			t.Fatalf("%q should fail", bad)
		}
	}
}

func TestCLIValidationExitsNonZero(t *testing.T) {
	if code := run(nil); code != 2 {
		t.Fatalf("no args exit = %d", code)
	}
	if code := run([]string{"bogus"}); code != 2 {
		t.Fatalf("bogus exit = %d", code)
	}
	if code := run([]string{"demo"}); code != 2 {
		t.Fatalf("missing out exit = %d", code)
	}
	if code := run([]string{"demo", "--out", t.TempDir(), "--bandwidth-kbps", "nope"}); code != 2 {
		t.Fatalf("bad bandwidth exit = %d", code)
	}
}

func TestCLISucceedsOnDemo(t *testing.T) {
	dir := t.TempDir()
	code := run([]string{"demo", "--generate", "--out", dir, "--loss-burst", "3", "--bandwidth-kbps", "900,420,1100"})
	if code != 0 {
		t.Fatalf("demo exit = %d", code)
	}
	for _, f := range []string{"manifest.json", "summary.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("%s missing: %v", f, err)
		}
	}
}
