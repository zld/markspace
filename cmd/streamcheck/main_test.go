package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"streamcheck/internal/fixture"
	"streamcheck/internal/manifest"
	"streamcheck/internal/probe"
)

func newFixture(t *testing.T) string {
	t.Helper()
	out := t.TempDir()
	err := fixture.Generate(fixture.Spec{
		Out: out, Rates: []int{800, 1600}, Segments: 6, SegmentSeconds: 4,
		Pattern: manifest.DefaultPattern,
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return out
}

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

type jsonReport struct {
	Rates []struct {
		RateKbps             int      `json:"rate_kbps"`
		SegmentCount         int      `json:"segment_count"`
		TotalDurationSeconds float64  `json:"total_duration_seconds"`
		DurationDeltaMS      int64    `json:"duration_delta_ms"`
		MissingRanges        []string `json:"missing_ranges"`
		DuplicateIDs         []int    `json:"duplicate_ids"`
		ValidationFailures   []string `json:"validation_failures"`
		ManifestSHA256       string   `json:"manifest_sha256"`
	} `json:"rates"`
	Warnings []string `json:"warnings"`
	ExitCode int      `json:"exit_code"`
	Failed   bool     `json:"failed"`
}

func decodeReport(t *testing.T, out string) jsonReport {
	t.Helper()
	var rep jsonReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("invalid JSON report: %v\n%s", err, out)
	}
	return rep
}

func TestBuildHappyPath(t *testing.T) {
	if err := probe.Available(""); err != nil {
		t.Skip(err)
	}
	dir := newFixture(t)
	code, out, errOut := runCLI(t, "build", "--input", dir, "--rates", "800,1600",
		"--segment-seconds", "4", "--tolerance-ms", "80", "--json")
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	rep := decodeReport(t, out)
	if rep.Failed || rep.ExitCode != 0 || len(rep.Rates) != 2 {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Warnings) != 0 {
		t.Fatalf("warnings = %v", rep.Warnings)
	}
	for _, rr := range rep.Rates {
		if rr.SegmentCount != 6 {
			t.Errorf("rate %d count = %d", rr.RateKbps, rr.SegmentCount)
		}
		if rr.TotalDurationSeconds < 23.9 || rr.TotalDurationSeconds > 24.1 {
			t.Errorf("rate %d total = %v", rr.RateKbps, rr.TotalDurationSeconds)
		}
		if rr.DurationDeltaMS > 80 {
			t.Errorf("rate %d delta = %d", rr.RateKbps, rr.DurationDeltaMS)
		}
		if len(rr.ValidationFailures) != 0 {
			t.Errorf("rate %d failures = %v", rr.RateKbps, rr.ValidationFailures)
		}
		if len(rr.ManifestSHA256) != 64 {
			t.Errorf("rate %d digest = %q", rr.RateKbps, rr.ManifestSHA256)
		}
		if _, err := os.Stat(filepath.Join(dir, "media_"+strconv.Itoa(rr.RateKbps)+".m3u8")); err != nil {
			t.Errorf("rate %d manifest: %v", rr.RateKbps, err)
		}
	}

	// A second run must ignore its own playlists and stay byte-identical.
	code, out2, _ := runCLI(t, "build", "--input", dir, "--rates", "800,1600", "--json")
	if code != exitOK {
		t.Fatalf("second run exit = %d", code)
	}
	rep2 := decodeReport(t, out2)
	if len(rep2.Warnings) != 0 {
		t.Fatalf("second run warnings = %v", rep2.Warnings)
	}
	for i := range rep.Rates {
		if rep.Rates[i].ManifestSHA256 != rep2.Rates[i].ManifestSHA256 {
			t.Fatal("manifest digest is not reproducible")
		}
	}
}

func TestBuildMissingLenientAndStrict(t *testing.T) {
	if err := probe.Available(""); err != nil {
		t.Skip(err)
	}
	dir := filepath.Join(newFixture(t), "cases", "missing")
	code, out, _ := runCLI(t, "build", "--input", dir, "--rates", "800,1600", "--json")
	if code != exitOK {
		t.Fatalf("lenient exit = %d", code)
	}
	rep := decodeReport(t, out)
	for _, rr := range rep.Rates {
		if len(rr.MissingRanges) != 1 || rr.MissingRanges[0] != "3-4" {
			t.Errorf("rate %d missing ranges = %v", rr.RateKbps, rr.MissingRanges)
		}
		if rr.SegmentCount != 4 || len(rr.ValidationFailures) != 0 {
			t.Errorf("rate %d = %+v", rr.RateKbps, rr)
		}
	}

	code, out, _ = runCLI(t, "build", "--input", dir, "--rates", "800,1600", "--strict", "--json")
	if code != exitInconsistent {
		t.Fatalf("strict exit = %d", code)
	}
	rep = decodeReport(t, out)
	if !rep.Failed || rep.ExitCode != 1 {
		t.Fatalf("strict report = %+v", rep)
	}
	for _, rr := range rep.Rates {
		if len(rr.ValidationFailures) == 0 || !strings.Contains(rr.ValidationFailures[0], "missing_segment_ids 3-4") {
			t.Errorf("rate %d failures = %v", rr.RateKbps, rr.ValidationFailures)
		}
	}
}

func TestBuildDuplicateAndDriftFail(t *testing.T) {
	if err := probe.Available(""); err != nil {
		t.Skip(err)
	}
	base := newFixture(t)
	for _, tc := range []struct {
		name    string
		check   func(jsonReport) bool
		noFiles bool
	}{
		{
			name: "duplicate",
			check: func(rep jsonReport) bool {
				return len(rep.Rates) == 2 && len(rep.Rates[0].DuplicateIDs) == 1 &&
					rep.Rates[0].DuplicateIDs[0] == 1
			},
			noFiles: true,
		},
		{
			name: "drift",
			check: func(rep jsonReport) bool {
				return rep.Rates[0].RateKbps == 800 && rep.Rates[1].DurationDeltaMS > 80 &&
					len(rep.Rates[1].ValidationFailures) == 1
			},
		},
	} {
		dir := filepath.Join(base, "cases", tc.name)
		code, out, _ := runCLI(t, "build", "--input", dir, "--rates", "800,1600", "--json")
		if code != exitInconsistent {
			t.Fatalf("%s: exit = %d", tc.name, code)
		}
		rep := decodeReport(t, out)
		if !rep.Failed || !tc.check(rep) {
			t.Errorf("%s: report = %+v", tc.name, rep)
		}
		if tc.noFiles {
			for _, rate := range []string{"800", "1600"} {
				if _, err := os.Stat(filepath.Join(dir, "media_"+rate+".m3u8")); !os.IsNotExist(err) {
					t.Errorf("duplicate case wrote media_%s.m3u8", rate)
				}
			}
		}
	}

	// Unordered input must sort numerically and pass.
	dir := filepath.Join(base, "cases", "unordered")
	code, out, _ := runCLI(t, "build", "--input", dir, "--rates", "800,1600", "--json")
	if code != exitOK {
		t.Fatalf("unordered exit = %d", code)
	}
	if rep := decodeReport(t, out); rep.Rates[0].SegmentCount != 12 {
		t.Fatalf("unordered report = %+v", rep)
	}
}

func TestBuildDetectsChangedFiles(t *testing.T) {
	dir := newFixture(t)
	// A fake ffprobe that appends a byte, simulating a writer that keeps
	// touching a segment after it was scanned.
	fake := filepath.Join(t.TempDir(), "ffprobe")
	script := "#!/bin/sh\nfor last; do :; done\nprintf 'x' >> \"$last\"\necho 4.000000\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCLI(t, "build", "--input", dir, "--rates", "800,1600",
		"--ffprobe", fake, "--json")
	if code != exitInconsistent {
		t.Fatalf("exit = %d", code)
	}
	rep := decodeReport(t, out)
	if !rep.Failed {
		t.Fatal("changed files were not reported")
	}
}
