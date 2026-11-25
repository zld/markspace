package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// runCapture invokes run() in-process and captures stdout/stderr. The exit
// code is returned directly, so the acceptance signals are tested exactly as
// the binary reports them.
func runCapture(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	code := run(args)
	outW.Close()
	errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	out := readAll(outR)
	errtxt := readAll(errR)
	return code, out, errtxt
}

func readAll(f *os.File) string {
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return b.String()
}

func field(t *testing.T, out, key string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimPrefix(line, key+"=")
		}
	}
	t.Fatalf("output missing %s= in:\n%s", key, out)
	return ""
}

func TestCLIValid(t *testing.T) {
	code, out, _ := runCapture(t, "--fixture", "valid", "--sample-rate", "48000",
		"--layout", "stereo", "--loudness", "-16", "--slow-consumer-ms", "20")
	if code != 0 {
		t.Fatalf("exit %d, out:\n%s", code, out)
	}
	if got := field(t, out, "stages"); got != "5" {
		t.Fatalf("stages=%s", got)
	}
	peak := field(t, out, "queue_peak_bytes")
	if peak == "" || peak == "0" {
		t.Fatalf("queue_peak_bytes=%s", peak)
	}
	peakN, err := strconv.Atoi(peak)
	if err != nil || peakN > 32000 {
		t.Fatalf("queue_peak_bytes=%s exceeds --memory-bytes", peak)
	}
	if got := field(t, out, "continuity_errors"); got != "0" {
		t.Fatalf("continuity_errors=%s", got)
	}
	if segs := field(t, out, "manifest_segments"); segs == "0" || segs == "" {
		t.Fatalf("manifest_segments=%s", segs)
	}
	if dig := field(t, out, "sha256"); len(dig) != 64 {
		t.Fatalf("sha256=%q", dig)
	}
}

func TestCLICorruptExitsInput(t *testing.T) {
	code, out, _ := runCapture(t, "--fixture", "corrupt", "--corrupt-block", "3")
	if code != exitInput {
		t.Fatalf("exit %d want %d", code, exitInput)
	}
	for _, key := range []string{"failed stage", "track_id", "input_offset"} {
		if !strings.Contains(out, key) {
			t.Fatalf("output missing %q:\n%s", key, out)
		}
	}
}

func TestCLIBoundaryTimestamp(t *testing.T) {
	code, out, _ := runCapture(t, "--fixture", "boundary", "--truncate-timestamp")
	if code != exitInput {
		t.Fatalf("exit %d want %d", code, exitInput)
	}
	if got := fieldLenient(out, "timestamp_errors"); got != "1" {
		t.Fatalf("timestamp_errors=%s", got)
	}
	if got := fieldLenient(out, "manifest_published"); got != "false" {
		t.Fatalf("manifest_published=%s", got)
	}
}

func TestCLIResumeMatchesPlain(t *testing.T) {
	dir := t.TempDir()
	cp := filepath.Join(dir, "cp.json")
	code, _, _ := runCapture(t, "--fixture", "valid", "--interrupt-after-block", "3", "--checkpoint", cp)
	if code != exitInput {
		t.Fatalf("interrupt exit %d", code)
	}
	code, out, _ := runCapture(t, "--fixture", "valid", "--interrupt-after-block", "3", "--checkpoint", cp, "--resume")
	if code != 0 {
		t.Fatalf("resume exit %d:\n%s", code, out)
	}
	if got := field(t, out, "resume_block"); got != "3" {
		t.Fatalf("resume_block=%s", got)
	}
	recomputed := field(t, out, "recomputed_blocks")
	if recomputed == "" || recomputed == "0" {
		t.Fatalf("recomputed_blocks=%s", recomputed)
	}
	resumedDigest := field(t, out, "manifest_sha256")

	code, plain, _ := runCapture(t, "--fixture", "valid")
	if code != 0 {
		t.Fatalf("plain exit %d", code)
	}
	if plainDigest := field(t, plain, "manifest_sha256"); plainDigest != resumedDigest {
		t.Fatalf("digest mismatch resumed=%s plain=%s", resumedDigest, plainDigest)
	}
}

func TestCLIGapFailsManifest(t *testing.T) {
	code, _, _ := runCapture(t, "--fixture", "gap")
	if code != exitManifest {
		t.Fatalf("exit %d want %d", code, exitManifest)
	}
}

func TestCLITamperCache(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	if code, _, _ := runCapture(t, "--fixture", "valid", "--cache", cacheDir); code != 0 {
		t.Fatalf("warm cache exit %d", code)
	}
	code, out, _ := runCapture(t, "--fixture", "valid", "--cache", cacheDir, "--tamper-cache")
	if code != 0 {
		t.Fatalf("tamper run exit %d:\n%s", code, out)
	}
	if got := field(t, out, "cache_rejected"); got != "1" {
		t.Fatalf("cache_rejected=%s", got)
	}
}

// TestExpectedTargetRebuildsFromFlags locks in that manifest verification is
// driven by the requested publish target rather than the manifest's own values.
func TestExpectedTargetRebuildsFromFlags(t *testing.T) {
	cases := []struct {
		layout   string
		channels uint16
	}{
		{"mono", 1},
		{"stereo", 2},
		{"5.1", 6},
		{"7.1", 8},
	}
	for _, c := range cases {
		f := &flags{sampleRate: 44100, layout: c.layout, loudness: -23.5}
		got := expectedTarget(f)
		if got.SampleRate != 44100 || got.Layout != c.layout || got.Channels != c.channels {
			t.Fatalf("%s target = %+v", c.layout, got)
		}
		if got.LoudnessLUFS != "-23.5" {
			t.Fatalf("loudness = %q, want -23.5", got.LoudnessLUFS)
		}
	}
}

// fieldLenient returns "" when the key is absent instead of failing.
func fieldLenient(out, key string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimPrefix(line, key+"=")
		}
	}
	return ""
}
