package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"streamlab/internal/report"
)

func TestUsageErrorsExit2(t *testing.T) {
	var buf bytes.Buffer
	if code := run(nil, &buf); code != exitUsage {
		t.Fatalf("no args: exit %d", code)
	}
	if code := run([]string{"bogus"}, &buf); code != exitUsage {
		t.Fatalf("bad subcommand: exit %d", code)
	}
	if code := run([]string{"simulate", "--frames", "0"}, &buf); code != exitUsage {
		t.Fatalf("bad frames: exit %d", code)
	}
	if code := run([]string{"simulate", "--nope"}, &buf); code != exitUsage {
		t.Fatalf("bad flag: exit %d", code)
	}
	if code := run([]string{"listen", "--addr", "8.8.8.8:9000"}, &buf); code != exitUsage {
		t.Fatalf("non-loopback addr: exit %d", code)
	}
}

func acceptanceArgs() []string {
	return strings.Split("simulate --seed 42 --frames 240 --frame-ms 20 "+
		"--base-delay-ms 45 --jitter-ms 18 --loss-burst 4 "+
		"--duplicate-rate 0.03 --reorder-rate 0.12 --json", " ")
}

func TestSimulateAcceptanceScenario(t *testing.T) {
	var buf bytes.Buffer
	if code := run(acceptanceArgs(), &buf); code != exitOK {
		t.Fatalf("exit %d, output: %s", code, buf.String())
	}
	var s report.Summary
	if err := json.Unmarshal(buf.Bytes(), &s); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got := len(s.BufferWatermarks); got != 240 {
		t.Fatalf("watermarks %d want 240", got)
	}
	if s.ReceivedPackets < 200 || s.ReceivedPackets > 240 {
		t.Fatalf("received %d outside expected range", s.ReceivedPackets)
	}
	if s.ConcealedFrames <= 0 || s.ConcealRunsMax <= 0 {
		t.Fatalf("expected concealment: %+v", s)
	}
	totalDropped := 0
	for _, n := range s.DroppedPackets {
		if n < 0 {
			t.Fatal("negative drop counter")
		}
		totalDropped += n
	}
	if totalDropped == 0 {
		t.Fatal("expected duplicates/reorders to be dropped")
	}
	// Fixed seed: a second run must produce byte-identical output.
	var buf2 bytes.Buffer
	if code := run(acceptanceArgs(), &buf2); code != exitOK {
		t.Fatalf("second run exit %d", code)
	}
	if !bytes.Equal(buf.Bytes(), buf2.Bytes()) {
		t.Fatal("same seed produced different output")
	}
}
