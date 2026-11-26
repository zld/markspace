package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRejectsBadArgumentsWithExitTwo(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != 2 {
		t.Fatalf("no args code=%d", code)
	}
	if code := run([]string{"demo", "--queue", "0"}, &out, &errOut); code != 2 {
		t.Fatalf("bad queue code=%d", code)
	}
	if code := run([]string{"frobnicate"}, &out, &errOut); code != 2 {
		t.Fatalf("unknown command code=%d", code)
	}
}

func TestDemoNormalCommandSucceeds(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{
		"demo", "--case", "normal", "--seed", "17", "--queue", "2",
		"--memory", "1048576", "--slow-ms", "0",
		"--workdir", t.TempDir(),
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s stdout=%s", code, errOut.String(), out.String())
	}
	if !strings.Contains(out.String(), `"case":"normal"`) || !strings.Contains(out.String(), `"ok":true`) {
		t.Fatalf("unexpected JSON output: %s", out.String())
	}
}
