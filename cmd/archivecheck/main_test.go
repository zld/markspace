package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDemoHealthyExitZero(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"demo", "--seed", "23", "--chunk-bytes", "7", "--queue", "2",
		"--target-kbps", "900,1800,3600", "--consumer-delay-ms", "15"}, &out, &errb)
	if code != exitOK {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	text := out.String()
	for _, field := range []string{
		"codec=", "units=", "keyframes=", "recoverable_gaps=", "target_kbps=",
		"actual_kbps=", "bitrate_error_pct=", "parse_ratio=",
		"mean_keyframe_interval_ms=", "max_keyframe_interval_ms=", "manifest_drift_ms=", "sha256=",
	} {
		if !strings.Contains(text, field) {
			t.Fatalf("missing %q", field)
		}
	}
}

func TestDemoCorruptIndexExitTwo(t *testing.T) {
	for _, c := range []string{"corrupt-index", "boundary-skew", "obu-truncated", "keyframe-regress"} {
		var out, errb bytes.Buffer
		code := run([]string{"demo", "--case", c}, &out, &errb)
		if code != exitRecover {
			t.Fatalf("%s exit=%d stderr=%s", c, code, errb.String())
		}
	}
}

func TestDemoCorruptIndexReportsKind(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"demo", "--case", "corrupt-index"}, &out, &errb); code != exitRecover {
		t.Fatalf("exit=%d", code)
	}
	text := out.String()
	if !strings.Contains(text, "index_out_of_range") || !strings.Contains(text, "recovered_segments=") {
		t.Fatalf("output missing required evidence:\n%s", text)
	}
}

func TestDemoBoundarySkewReportsConflict(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"demo", "--case", "boundary-skew"}, &out, &errb); code != exitRecover {
		t.Fatalf("exit=%d", code)
	}
	text := out.String()
	if !strings.Contains(text, "boundary_conflict") || !strings.Contains(text, "boundary_diff") {
		t.Fatalf("output missing conflict evidence:\n%s", text)
	}
}

func TestDemoRepeatsShareDigest(t *testing.T) {
	digest := func() string {
		var out, errb bytes.Buffer
		run([]string{"demo", "--seed", "23", "--chunk-bytes", "7", "--queue", "2",
			"--target-kbps", "900,1800,3600", "--consumer-delay-ms", "15"}, &out, &errb)
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.HasPrefix(line, "sha256=") {
				return line
			}
		}
		return ""
	}
	a, b := digest(), digest()
	if a == "" || a != b {
		t.Fatalf("digest differs: %q vs %q", a, b)
	}
}

func TestCLIErrors(t *testing.T) {
	cases := [][]string{
		{},
		{"demo", "--case", "nope"},
		{"demo", "--target-kbps", "900"},
		{"demo", "--target-kbps", "900,0,3600"},
		{"demo", "--chunk-bytes", "0"},
		{"demo", "--queue", "0"},
		{"demo", "extra"},
		{"bogus"},
	}
	for _, args := range cases {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != exitError {
			t.Fatalf("args=%q exit=%d", args, code)
		}
	}
}

func TestHelpAndJSON(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"help"}, &out, &errb); code != exitOK {
		t.Fatalf("help exit=%d", code)
	}
	if !strings.Contains(out.String(), "archivecheck demo") {
		t.Fatalf("help=%q", out.String())
	}

	out.Reset()
	if code := run([]string{"demo", "--json"}, &out, &errb); code != exitOK {
		t.Fatalf("json exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), `"sha256"`) || !strings.Contains(out.String(), `"layers"`) {
		t.Fatalf("json output missing fields")
	}
}
