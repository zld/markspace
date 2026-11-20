package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// runCLI executes the CLI in-process and captures output.
func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func fixturePath(t *testing.T, profile string) string {
	t.Helper()
	p := t.TempDir() + "/" + profile + ".mp4"
	code, _, errb := runCLI(t, "fixture", "--profile", profile, "--out", p)
	if code != 0 {
		t.Fatalf("fixture %s: exit %d: %s", profile, code, errb)
	}
	return p
}

func TestAcceptanceFlow(t *testing.T) {
	edit := fixturePath(t, "edit-wrap")

	// inspect: 2 tracks, 12 video samples.
	code, out, errb := runCLI(t, "inspect", "--input", edit, "--json")
	if code != 0 {
		t.Fatalf("inspect: %d %s", code, errb)
	}
	var summary struct {
		MovieTimescale uint32 `json:"movie_timescale"`
		Tracks         int    `json:"tracks"`
		VideoSamples   int    `json:"video_samples"`
	}
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.MovieTimescale != 90000 || summary.Tracks != 2 || summary.VideoSamples != 12 {
		t.Fatalf("inspect summary: %+v", summary)
	}

	// seek: anchor sample 1, IDR, movie 200ms.
	code, out, errb = runCLI(t, "seek", "--input", edit, "--track", "1", "--at-ms", "250")
	if code != 0 {
		t.Fatalf("seek: %d %s", code, errb)
	}
	for _, want := range []string{
		"requested_ms=250", "anchor_sample=1", "pts_ticks=90000",
		"timescale=90000", "movie_pts_ms=200", "type=IDR", "sha256=",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("seek output missing %q:\n%s", want, out)
		}
	}

	// export: 11 records in [200,500) ms.
	manifest := t.TempDir() + "/gop.jsonl"
	code, _, errb = runCLI(t, "export", "--input", edit, "--track", "1",
		"--from-ms", "200", "--to-ms", "500", "--out", manifest)
	if code != 0 {
		t.Fatalf("export: %d %s", code, errb)
	}
	code, out, _ = runCLI(t, "index", "--input", edit, "--track", "1", "--jsonl")
	if code != 0 {
		t.Fatalf("index: %d", code)
	}
	if !strings.Contains(out, `"wrap_events":0`) {
		t.Fatalf("classic index should report wrap_events=0:\n%s", out)
	}
}

func TestFragmentedWrapAcceptance(t *testing.T) {
	frag := fixturePath(t, "fragmented-wrap")
	code, out, errb := runCLI(t, "index", "--input", frag, "--track", "1", "--jsonl")
	if code != 0 {
		t.Fatalf("index: %d %s", code, errb)
	}
	if !strings.Contains(out, `"wrap_events":1`) {
		t.Fatalf("want wrap_events=1:\n%s", out)
	}
	var summary struct {
		MaxDTS int64 `json:"max_dts"`
	}
	first, _, _ := strings.Cut(out, "\n")
	if err := json.Unmarshal([]byte(first), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.MaxDTS <= 4294967295 {
		t.Fatalf("max_dts %d not above 2^32-1", summary.MaxDTS)
	}
}

func TestExitCodes(t *testing.T) {
	trunc := fixturePath(t, "truncated")
	if code, _, _ := runCLI(t, "inspect", "--input", trunc, "--json"); code != 3 {
		t.Fatalf("truncated: exit %d, want 3", code)
	}
	oob := fixturePath(t, "oob")
	if code, _, _ := runCLI(t, "inspect", "--input", oob, "--json"); code != 3 {
		t.Fatalf("oob: exit %d, want 3", code)
	}
	edit := fixturePath(t, "edit-wrap")
	if code, _, _ := runCLI(t, "seek", "--input", edit, "--track", "1", "--at-ms", "10"); code != 4 {
		t.Fatalf("early seek: exit %d, want 4", code)
	}
	if code, _, _ := runCLI(t, "seek", "--input", edit, "--track", "1", "--at-ms", "99999"); code != 4 {
		t.Fatalf("late seek: exit %d, want 4", code)
	}
	if code, _, _ := runCLI(t, "seek", "--input", edit); code != 2 {
		t.Fatalf("usage: exit %d, want 2", code)
	}
	if code, _, _ := runCLI(t, "inspect", "--input", t.TempDir()+"/missing.mp4"); code != 2 {
		t.Fatalf("io: exit %d, want 2", code)
	}
	if code, _, _ := runCLI(t, "seek", "--input", edit, "--track", "2", "--at-ms", "250"); code != 3 {
		t.Fatalf("non-AVC track: exit %d, want 3", code)
	}
}
