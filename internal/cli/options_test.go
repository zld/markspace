package cli

import (
	"bytes"
	"strings"
	"testing"
)

func baseArgs(extra ...string) []string {
	args := []string{"demo",
		"--duration", "120", "--segment-seconds", "10",
		"--target-kbps", "2400", "--workers", "3",
		"--checkpoint", "/tmp/ws/job.json",
		"--encoder", "enc --in {in} --out {out} --target-kbps {kbps} --duration-sec {duration}"}
	return append(args, extra...)
}

func TestParseValid(t *testing.T) {
	o, err := Parse(baseArgs())
	if err != nil {
		t.Fatal(err)
	}
	if o.DurationSec != 120 || o.SegmentSec != 10 || o.TargetKbps != 2400 || o.Workers != 3 {
		t.Fatalf("parsed values wrong: %+v", o)
	}
	if o.Workspace != "/tmp/ws" || o.InputPath != "/tmp/ws/sample.vid" {
		t.Fatalf("path resolution wrong: %+v", o)
	}
}

func TestParseValidationFailures(t *testing.T) {
	cases := map[string][]string{
		"missing subcommand": {},
		"bad subcommand":     {"frobnicate"},
		"negative duration":  baseArgs("--duration", "-1"),
		"zero segment":       baseArgs("--segment-seconds", "0"),
		"segment > duration": baseArgs("--segment-seconds", "999"),
		"zero target":        baseArgs("--target-kbps", "0"),
		"zero workers":       baseArgs("--workers", "0"),
		"no checkpoint": {"demo", "--duration", "1", "--segment-seconds", "1",
			"--target-kbps", "100", "--encoder", "e {in} {out}"},
		"no encoder": {"demo", "--duration", "1", "--segment-seconds", "1",
			"--target-kbps", "100", "--checkpoint", "/tmp/x/job.json"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(args); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestParseUnknownFlag(t *testing.T) {
	if _, err := Parse(baseArgs("--nope", "x")); err == nil {
		t.Fatal("unknown flag must error")
	}
}

func TestJobIDStable(t *testing.T) {
	a, _ := Parse(baseArgs())
	b, _ := Parse(baseArgs())
	if jobID(a) != jobID(b) || jobID(a) == "" {
		t.Fatal("job id must be deterministic")
	}
}

func TestReportWriters(t *testing.T) {
	r := JSONReport{
		Status: "success", JobID: "job-x", Segments: 2, Succeeded: 2,
		TargetKbps: 2400, ActualKbps: 2395, DeviationPct: -0.208,
		PSNRMeanDB: 38.5, WorstSegment: 2, WorstSegmentPSNRDB: 33.1,
		SegmentPSNRDB:  []float64{40, 33.1},
		SegmentDeltaDB: []float64{6.9},
		SegmentsDetail: []SegmentDetailJSON{
			{Segment: 1, Status: "succeeded", TargetKbps: 2400, ActualKbps: 2395,
				DeviationPct: -0.2, Attempts: 1, PSNRMeanDB: 40},
		},
	}
	var buf bytes.Buffer
	if err := WriteJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"segments": 2`) ||
		!strings.Contains(buf.String(), `"segment_psnr_delta_db"`) {
		t.Fatalf("JSON missing required fields:\n%s", buf.String())
	}
	buf.Reset()
	WriteText(&buf, r)
	for _, want := range []string{"segments=2", "target_kbps=2400", "actual_kbps=2395",
		"psnr_mean_db=38.5000", "segment_psnr_db=", "segment_psnr_delta_db="} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("text report missing %q:\n%s", want, buf.String())
		}
	}
}

func TestFormatFloatEdgeCases(t *testing.T) {
	if formatFloat(nanFloat()) != "NaN" {
		t.Fatal("NaN must render as NaN, never pollute output")
	}
	if formatFloat(infFloat()) != "+Inf" {
		t.Fatal("+Inf rendering wrong")
	}
}
