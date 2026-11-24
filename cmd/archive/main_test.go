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

func TestCLIProbeReportsExitClasses(t *testing.T) {
	dir := t.TempDir()
	if code := run([]string{"demo", "--generate", "--out", dir}); code != 0 {
		t.Fatalf("setup demo exit = %d", code)
	}
	cases := []struct {
		name string
		want int
	}{
		{"assets/normal.mp4", 0},
		{"assets/normal.mxf", 0},
		{"assets/boundary.mp4", 0},
		{"assets/corrupt.mp4", 3},
		{"assets/invalid-crop.mp4", 4},
	}
	for _, c := range cases {
		if got := run([]string{"probe", filepath.Join(dir, c.name)}); got != c.want {
			t.Fatalf("probe %s exit = %d, want %d", c.name, got, c.want)
		}
	}
	if got := run([]string{"probe"}); got != 2 {
		t.Fatalf("probe without file exit = %d", got)
	}
	if got := run([]string{"probe", filepath.Join(dir, "missing.mp4")}); got != 5 {
		t.Fatalf("probe missing file exit = %d", got)
	}
}
