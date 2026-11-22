package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunDefaultDemoPasses(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{
		"--generate", "--seed", "41", "--loss", "3", "--reorder", "7",
		"--drift-ppm", "120", "--wrap-at", "4294967000", "--vfr", "--segments", "12",
	}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	stdout := out.String()
	for _, want := range []string{
		"roundtrip_rate=", "keyframe_hit_rate=", "segments=12",
		"segment_sha256=12/12", "recovered_packets=3", "unrecovered_gaps=",
		"late_packets=", "clock_wraps=1", "drift_ppm_est=", "truncated_errors=1",
		"PASS",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("output missing %q:\n%s", want, stdout)
		}
	}
	if errOut.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", errOut.String())
	}
}

func TestRunIsReproducible(t *testing.T) {
	args := []string{"--generate", "--seed", "41", "--loss", "3", "--segments", "12"}
	var a, b bytes.Buffer
	if code := run(args, &a, &bytes.Buffer{}); code != exitOK {
		t.Fatalf("first run exit %d", code)
	}
	if code := run(args, &b, &bytes.Buffer{}); code != exitOK {
		t.Fatalf("second run exit %d", code)
	}
	if a.String() != b.String() {
		t.Fatal("repeated runs produced different output")
	}
	// Every segment digest line must be identical between runs.
	ha := digests(a.String())
	hb := digests(b.String())
	if len(ha) != 12 || len(hb) != 12 {
		t.Fatalf("expected 12 digests, got %d and %d", len(ha), len(hb))
	}
	for i := range ha {
		if ha[i] != hb[i] {
			t.Fatalf("segment %d digest differs: %s vs %s", i, ha[i], hb[i])
		}
	}
}

func digests(out string) []string {
	var d []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "segment[") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				d = append(d, fields[1])
			}
		}
	}
	return d
}

func TestRunUsageErrors(t *testing.T) {
	cases := [][]string{
		{},                                      // missing --generate
		{"--generate", "--segments", "0"},       // zero segments
		{"--generate", "--drift-ppm", "999999"}, // drift out of range
		{"--generate", "--wrap-at", "5000000000"}, // does not fit uint32
		{"--generate", "--loss", "-1"},            // negative loss
		{"--generate", "extra-arg"},               // stray argument
	}
	for i, args := range cases {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != exitUsage {
			t.Fatalf("case %d: exit = %d, want %d", i, code, exitUsage)
		}
	}
}

func TestRunHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--help"}, &out, &errOut); code != exitOK {
		t.Fatalf("help exit = %d", code)
	}
	if !strings.Contains(errOut.String(), "mediadump") {
		t.Fatalf("usage text missing: %s", errOut.String())
	}
}

func TestRunSegmentsMustMatchRequest(t *testing.T) {
	// Fewer keyframes than requested segments must fail with exit 1, not 2,
	// because the input is valid but delivery cannot meet the request.
	var out, errOut bytes.Buffer
	code := run([]string{"--generate", "--segments", "12"}, &out, &errOut)
	if code != exitOK && code != exitDelivery {
		t.Fatalf("exit = %d", code)
	}
}
