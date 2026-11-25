package main

import (
	"bytes"
	"testing"
)

func TestRunNormalDemoPassesAndPrintsFixedFields(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"demo", "--seed", "7"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code=%d stderr=%s", code, errOut.String())
	}
	text := out.String()
	for _, field := range []string{
		`"sample"`, `"track"`, `"input_offset"`, `"time_delta_ns"`, `"byte_delta"`,
		`"failure_offset"`, `"error_code"`, `"sha256"`,
	} {
		if !bytes.Contains(out.Bytes(), []byte(field)) {
			t.Fatalf("output missing field %s", field)
		}
	}
	if !bytes.Contains(out.Bytes(), []byte(`"errors": 0`)) {
		t.Fatalf("normal demo should have zero errors: %s", text)
	}
}

func TestRunTruncatedClusterFailsWithStableLocation(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"demo", "--case", "truncated-cluster"}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit=%d want 1", code)
	}
	text := out.String()
	for _, want := range []string{
		`"element_id": "0x1F43B675"`,
		`"input_offset": 177`,
		`"failure_offset": 366`,
		`"error_code": "EBML_TRUNCATED_ELEMENT"`,
		`"error_category": "truncated_element"`,
	} {
		if !bytes.Contains(out.Bytes(), []byte(want)) {
			t.Fatalf("output missing %s\n%s", want, text)
		}
	}
}

func TestRunAudioConflictFails(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"demo", "--case", "audio-conflict"}, &out, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("exit=%d want 1", code)
	}
	text := out.String()
	if !bytes.Contains(out.Bytes(), []byte("TRACK_LAYOUT_CONFLICT")) ||
		!bytes.Contains(out.Bytes(), []byte("TRACK_SAMPLE_RATE_CONFLICT")) {
		t.Fatalf("expected both audio conflicts: %s", text)
	}
}

func TestRunRTPGapFailsButReportsRecovery(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"demo", "--case", "rtp-gap"}, &out, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("exit=%d want 1", code)
	}
	text := out.String()
	for _, want := range []string{
		`"rtp_sequence": 2001`,
		`"time_start_ns": 0`,
		`"time_end_ns": 40000000`,
		`"recovered_missing_packets": 1`,
	} {
		if !bytes.Contains(out.Bytes(), []byte(want)) {
			t.Fatalf("output missing %s\n%s", want, text)
		}
	}
}

func TestRunHelpReturns0(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--help"}, &out, &errOut); code != 0 {
		t.Fatalf("help code=%d want0", code)
	}
	if out.Len() == 0 {
		t.Fatal("help output is empty")
	}
}

func TestRunInvalidArgumentsReturns2(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--unknown"}, &out, &errOut); code != 2 {
		t.Fatalf("root invalid code=%d want2", code)
	}
	if code := run([]string{"demo", "--case", "nope"}, &out, &errOut); code != 2 {
		t.Fatalf("bad case code=%d want2", code)
	}
}
