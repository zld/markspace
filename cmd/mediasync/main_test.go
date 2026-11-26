package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediasync/internal/fixtures"
)

func runCLI(t *testing.T, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = run(args, &o, &e)
	return code, o.String(), e.String()
}

func writeSRT(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeMP4(t *testing.T, dir, name string, keyframes []int) string {
	t.Helper()
	n := 50
	deltas := make([]uint32, n)
	sizes := make([]uint32, n)
	for i := 0; i < n; i++ {
		deltas[i] = 1000 // 25 fps at timescale 25000
		sizes[i] = 200
	}
	p := filepath.Join(dir, name)
	_, err := fixtures.WriteMP4(p, fixtures.MP4Spec{
		Timescale: 25000, SampleDelta: deltas, SampleSizes: sizes,
		Keyframes: keyframes,
	}, 7)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDemoExitZeroAndInvariants(t *testing.T) {
	code, out, errOut := runCLI(t, "demo", "--seed", "7", "--late-window", "2s")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s\n%s", code, errOut, out)
	}
	if strings.Contains(out, "ACCEPTANCE FAIL") {
		t.Fatalf("acceptance failure:\n%s", out)
	}
	for _, want := range []string{
		"sample: normal", "sample: truncated-mp4", "sample: corrupt-rtp",
		"sample: illegal-df", "sample: late-in-window", "sample: late-out-of-window",
		"sha256:", "first failure:", "damaged byte range:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("demo output missing %q", want)
		}
	}
}

func TestDemoRepeatedRunsIdentical(t *testing.T) {
	_, a, _ := runCLI(t, "demo", "--seed", "7", "--late-window", "2s")
	_, b, _ := runCLI(t, "demo", "--seed", "7", "--late-window", "2s")
	if a != b {
		t.Fatal("demo output differs between identical runs")
	}
}

func TestDemoBadParameters(t *testing.T) {
	for _, args := range [][]string{
		{"demo", "--late-window", "bogus"},
		{"demo", "--late-window", "-1s"},
		{"demo", "--seed", "abc"},
		{"demo", "extra"},
	} {
		if code, _, _ := runCLI(t, args...); code != 2 {
			t.Fatalf("%v exit=%d want 2", args, code)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	if code, _, _ := runCLI(t, "frobnicate"); code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if code, _, _ := runCLI(t); code != 2 {
		t.Fatalf("no-args exit=%d", code)
	}
}

func TestCheckPassAndFail(t *testing.T) {
	dir := t.TempDir()
	mp4 := writeMP4(t, dir, "ok.mp4", []int{1, 26})
	good := writeSRT(t, dir, "good.srt", "1\n00:00:00,000 --> 00:00:00,800\nA\n\n2\n00:00:01,000 --> 00:00:01,800\nB\n")
	if code, out, errOut := runCLI(t, "check", "--mp4", mp4, "--srt", good); code != 0 {
		t.Fatalf("exit=%d stderr=%s\n%s", code, errOut, out)
	}
	bad := writeSRT(t, dir, "bad.srt", "1\n00:00:00,000 --> 00:00:09,000\nA\n\n2\n00:00:05,000 --> 00:00:06,000\nB\n")
	code, out, errOut := runCLI(t, "check", "--mp4", mp4, "--srt", bad)
	if code != 1 {
		t.Fatalf("exit=%d stderr=%s\n%s", code, errOut, out)
	}
	if !strings.Contains(out, "overlapping_cue") {
		t.Fatalf("missing overlap report:\n%s", out)
	}
}

func TestCheckDigestStable(t *testing.T) {
	dir := t.TempDir()
	mp4 := writeMP4(t, dir, "ok.mp4", []int{1, 26})
	srt := writeSRT(t, dir, "a.srt", "1\n00:00:00,000 --> 00:00:00,800\nA\n")
	_, a, _ := runCLI(t, "digest", "--mp4", mp4, "--srt", srt)
	_, b, _ := runCLI(t, "digest", "--mp4", mp4, "--srt", srt)
	if a != b || len(strings.TrimSpace(a)) != 64 {
		t.Fatalf("digest not stable: %q vs %q", a, b)
	}
}

func TestCheckMissingInputIsExecFailure(t *testing.T) {
	if code, _, _ := runCLI(t, "check"); code != 2 {
		t.Fatalf("no inputs exit=%d", code)
	}
	dir := t.TempDir()
	if code, _, _ := runCLI(t, "check", "--mp4", filepath.Join(dir, "nope.mp4")); code != 2 {
		t.Fatalf("missing file exit=%d", code)
	}
}

func TestCheckVTT(t *testing.T) {
	dir := t.TempDir()
	mp4 := writeMP4(t, dir, "ok.mp4", []int{1, 26})
	vtt := writeSRT(t, dir, "a.vtt", "WEBVTT\n\n00:00.000 --> 00:00.800\nA\n")
	code, out, _ := runCLI(t, "check", "--mp4", mp4, "--vtt", vtt)
	if code != 0 {
		t.Fatalf("exit=%d\n%s", code, out)
	}
	if !strings.Contains(out, "cue mapping") {
		t.Fatalf("no cue mapping:\n%s", out)
	}
}

func TestHelp(t *testing.T) {
	if code, out, _ := runCLI(t, "--help"); code != 0 || !strings.Contains(out, "Usage:") {
		t.Fatalf("help exit=%d", code)
	}
}
