package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dashforge/internal/report"
)

// prepareFixtures materializes the fixture set in a temp directory and
// returns the absolute paths used by each test case.
func prepareFixtures(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if code := run([]string{"fixtures", "--out", dir, "--with-files"},
		new(bytes.Buffer), new(bytes.Buffer)); code != exitOK {
		t.Fatalf("fixtures subcommand exited %d", code)
	}
	return dir
}

func TestGenerateAndValidateHappyPath(t *testing.T) {
	dir := prepareFixtures(t)
	inputPath := filepath.Join(dir, "valid.json")
	mpdPath := filepath.Join(dir, "out.mpd")

	var out, errBuf bytes.Buffer
	code := run([]string{
		"generate", "--input", inputPath, "--output", mpdPath,
		"--timescale", "90000", "--tolerance-ms", "20",
	}, &out, &errBuf)
	if code != exitOK {
		t.Fatalf("generate exit=%d stderr=%s", code, errBuf.String())
	}
	summary := out.String()
	for _, want := range []string{
		"representations=3", "segments=36", "timescale=90000",
		"duration_ms=", "max_alignment_error_ms=",
		"first=", "last=",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q:\n%s", want, summary)
		}
	}
	if errBuf.Len() != 0 {
		t.Errorf("unexpected stderr: %s", errBuf.String())
	}
	if _, err := os.Stat(mpdPath); err != nil {
		t.Fatalf("MPD not written: %v", err)
	}

	// validate --json must round-trip into the stable Summary shape.
	var jout, jerr bytes.Buffer
	code = run([]string{
		"validate", "--input", inputPath, "--mpd", mpdPath,
		"--timescale", "90000", "--tolerance-ms", "20", "--json",
	}, &jout, &jerr)
	if code != exitOK {
		t.Fatalf("validate exit=%d stderr=%s", code, jerr.String())
	}
	var sum report.Summary
	if err := json.Unmarshal(jout.Bytes(), &sum); err != nil {
		t.Fatalf("summary is not valid JSON: %v\n%s", err, jout.String())
	}
	if sum.Status != "ok" || sum.Representations != 3 || sum.Segments != 36 ||
		sum.Timescale != 90000 || sum.MaxAlignmentErrorMs > 20 {
		t.Fatalf("unexpected summary: %+v", sum)
	}
	if len(sum.Renditions) != 3 || sum.Renditions[0].SegmentCount != 12 ||
		sum.Renditions[0].FirstSegment == "" || sum.Renditions[2].LastSegment == "" {
		t.Fatalf("bad rendition summaries: %+v", sum.Renditions)
	}
}

func TestNegativeFixturesExitInvalid(t *testing.T) {
	dir := prepareFixtures(t)
	cases := map[string]string{
		"negative-offset.json":         "boundary",
		"negative-missing-number.json": "number_gap",
		"negative-duration-drift.json": "duration_drift",
	}
	for file, wantKind := range cases {
		t.Run(file, func(t *testing.T) {
			var out, errBuf bytes.Buffer
			code := run([]string{
				"generate", "--input", filepath.Join(dir, file),
				"--output", filepath.Join(dir, "should-not-exist-"+file+".mpd"),
				"--timescale", "90000", "--tolerance-ms", "20",
			}, &out, &errBuf)
			if code != exitInvalid {
				t.Fatalf("exit=%d, want %d; stderr=%s", code, exitInvalid, errBuf.String())
			}
			combined := out.String() + errBuf.String()
			if !strings.Contains(combined, wantKind) {
				t.Fatalf("output missing %q:\n%s", wantKind, combined)
			}
			if _, err := os.Stat(filepath.Join(dir, "should-not-exist-"+file+".mpd")); !os.IsNotExist(err) {
				t.Fatal("invalid run must not write an MPD")
			}
		})
	}
}

func TestArgumentErrors(t *testing.T) {
	dir := prepareFixtures(t)
	valid := filepath.Join(dir, "valid.json")
	cases := [][]string{
		{"generate"},
		{"generate", "--input", valid, "--tolerance-ms", "-1"},
		{"generate", "--input", valid, "--timescale", "0"},
		{"validate", "--input", valid},
		{"bogus"},
	}
	for _, args := range cases {
		var out, errBuf bytes.Buffer
		if code := run(args, &out, &errBuf); code != exitArgs {
			t.Fatalf("args %v exited %d, want %d", args, code, exitArgs)
		}
		if errBuf.Len() == 0 {
			t.Errorf("args %v produced no stderr", args)
		}
	}
}

func TestIOFailuresMapExitCodeFour(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run([]string{"generate", "--input", "/no/such/metadata.json"}, &out, &errBuf)
	if code != exitIOFailure {
		t.Fatalf("missing input exit=%d, want %d", code, exitIOFailure)
	}
	if !strings.Contains(errBuf.String(), "read metadata") {
		t.Fatalf("stderr should explain the read failure: %s", errBuf.String())
	}

	dir := prepareFixtures(t)
	var vout, verr bytes.Buffer
	code = run([]string{
		"validate", "--input", filepath.Join(dir, "valid.json"),
		"--mpd", filepath.Join(dir, "missing.mpd"),
	}, &vout, &verr)
	if code != exitInvalid {
		t.Fatalf("missing MPD exit=%d, want %d", code, exitInvalid)
	}
	if !strings.Contains(vout.String()+verr.String(), "mpd_read") {
		t.Fatal("expected mpd_read finding for missing MPD file")
	}
}

func TestGenerateToStdout(t *testing.T) {
	dir := prepareFixtures(t)
	var out, errBuf bytes.Buffer
	code := run([]string{
		"generate", "--input", filepath.Join(dir, "valid.json"),
		"--timescale", "90000",
	}, &out, &errBuf)
	if code != exitOK {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "<MPD") || !strings.Contains(out.String(), "</MPD>") {
		t.Fatal("stdout should carry the MPD document")
	}
}

func TestCheckFilesWithPlaceholders(t *testing.T) {
	dir := prepareFixtures(t)
	// Segment roots in fixtures are relative ("media"), so run from the
	// fixture directory; restore the working directory afterwards.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	var gout, gerr bytes.Buffer
	if code := run([]string{
		"generate", "--input", "valid.json", "--output", "out.mpd",
	}, &gout, &gerr); code != exitOK {
		t.Fatalf("generate in fixture dir failed: %s", gerr.String())
	}

	// Every placeholder exists on disk, so the full cross-check must pass.
	var out, errBuf bytes.Buffer
	code := run([]string{
		"validate", "--input", "valid.json", "--mpd", "out.mpd",
		"--check-files",
	}, &out, &errBuf)
	if code != exitOK {
		t.Fatalf("check-files validation failed: %s%s", out.String(), errBuf.String())
	}
	if strings.Contains(out.String()+errBuf.String(), "unresolved_reference") {
		t.Fatalf("placeholder files should resolve: %s%s", out.String(), errBuf.String())
	}
}
