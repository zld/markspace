package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIBadFlagAndUnknownScenarioExit2(t *testing.T) {
	var out, err bytes.Buffer
	if code := run([]string{"-no-such-flag"}, &out, &err); code != 2 {
		t.Fatalf("bad flag code=%d", code)
	}
	out.Reset()
	err.Reset()
	if code := run([]string{"-scenario", "does-not-exist", "-root", t.TempDir()}, &out, &err); code != 2 {
		t.Fatalf("unknown scenario code=%d", code)
	}
	if !strings.Contains(out.String(), "unknown_scenario") {
		t.Fatalf("output=%q", out.String())
	}
}

func TestCLIInterrupt75AndResume0(t *testing.T) {
	root := filepath.Join(t.TempDir(), "recovery")
	var out bytes.Buffer
	code := run([]string{"-scenario", "interrupt", "-after-block", "1", "-root", root}, &out, &out)
	if code != 75 {
		t.Fatalf("interrupt code=%d output=%s", code, out.String())
	}
	if !strings.Contains(out.String(), "committed_blocks=1") {
		t.Fatalf("missing checkpoint output: %s", out.String())
	}
	out.Reset()
	code = run([]string{"-scenario", "resume", "-root", root}, &out, &out)
	if code != 0 {
		t.Fatalf("resume code=%d output=%s", code, out.String())
	}
	if !strings.Contains(out.String(), "index_entries=12") || !strings.Contains(out.String(), "repeat_hash_match=true") {
		t.Fatalf("bad resume output: %s", out.String())
	}
}
