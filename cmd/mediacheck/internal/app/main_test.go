package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunDemoAndVerify drives the full acceptance path in-process and checks
// the documented terminal fields and exit codes.
func TestRunDemoAndVerify(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	code, stdout := runCaptured(t, context.Background(), []string{
		"demo", "--seed", "41", "--workers", "4", "--queue", "8", "--out", dir,
	}, &out, &errOut)
	if code != ExitOK {
		t.Fatalf("demo exit = %d, stderr = %s", code, errOut.String())
	}
	stdout += out.String()
	for _, want := range []string{
		"roundtrip_parse=100.00%",
		"keyframe_hit=100.00%",
		"segment_sha256_match=100.00%",
		"tracks=2",
		"segments=8",
		"duplicates=3",
		"reordered=5",
		"lost=2",
		"cancelled_tasks=4",
		"repeat_manifest_match=true",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("demo output missing %q\n%s", want, stdout)
		}
	}
	// verify the produced pair
	out.Reset()
	errOut.Reset()
	code, stdout = runCaptured(t, context.Background(), []string{
		"verify", "--input", filepath.Join(dir, "source.mov"), "--output", filepath.Join(dir, "remux.mp4"),
	}, &out, &errOut)
	if code != ExitOK {
		t.Fatalf("verify exit = %d, stderr = %s", code, errOut.String())
	}
	stdout += out.String()
	if !strings.Contains(stdout, "roundtrip_parse=100.00%") || !strings.Contains(stdout, "keyframe_hit=100.00%") {
		t.Errorf("verify output unexpected:\n%s", stdout)
	}
	if !strings.Contains(stdout, "sample_match=true") {
		t.Errorf("verify did not confirm byte-identical samples:\n%s", stdout)
	}
}

func TestExitCodes(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no args", nil, ExitUsage},
		{"unknown command", []string{"bogus"}, ExitUsage},
		{"bad workers", []string{"demo", "--out", t.TempDir(), "--workers", "0"}, ExitUsage},
		{"missing out", []string{"demo"}, ExitUsage},
		{"verify missing flags", []string{"verify"}, ExitUsage},
		{"verify missing file", []string{"verify", "--input", "/definitely/not/here.mov", "--output", "/tmp/x.mp4"}, ExitCorrupt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if got, _ := runCaptured(t, context.Background(), tc.args, &out, &errOut); got != tc.want {
				t.Errorf("exit = %d, want %d (stderr=%s)", got, tc.want, errOut.String())
			}
		})
	}
}

func TestCancelledContextExits130(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer
	code, _ := runCaptured(t, ctx, []string{"demo", "--out", t.TempDir()}, &out, &errOut)
	if code != ExitCancelled {
		t.Errorf("exit = %d, want %d", code, ExitCancelled)
	}
}

// runCaptured swaps os.Stdout/os.Stderr for the duration of the call so the
// terminal field format can be asserted.
func runCaptured(t *testing.T, ctx context.Context, args []string, stdout, stderr *bytes.Buffer) (int, string) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	os.Stderr = w
	done := make(chan string)
	go func() {
		var b bytes.Buffer
		b.ReadFrom(r)
		done <- b.String()
	}()
	code := RunWithContext(ctx, args)
	w.Close()
	os.Stdout, os.Stderr = origOut, origErr
	return code, <-done
}
