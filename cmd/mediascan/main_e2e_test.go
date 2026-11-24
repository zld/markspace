package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEndToEndAcceptance rebuilds the CLI as a subprocess and asserts the
// documented exit codes and output invariants for every acceptance case.
func TestEndToEndAcceptance(t *testing.T) {
	binary := buildTestBinary(t)
	cases := []struct {
		name        string
		args        []string
		wantCode    int
		mustContain []string
	}{
		{
			name:     "normal",
			args:     []string{"--generate", "--case", "normal", "--format", "hls,dash"},
			wantCode: 0,
			mustContain: []string{
				"roundtrip_consistency=1.000",
				"keyframe_hit_rate=1.000",
				"damage_count=0",
				"input_sha256=",
				"exit_code=0",
			},
		},
		{
			name:     "damaged",
			args:     []string{"--generate", "--case", "damaged", "--truncate-box", "17", "--timestamp-wrap", "--vfr", "--drift-ppm", "120"},
			wantCode: 2,
			mustContain: []string{
				"truncated_box",
				"damage_offset=",
				"isolated_range=",
				"verified_prefix_bytes=",
				"processable_after_damage=2",
				"wrap_events=1",
				"exit_code=2",
			},
		},
		{
			name:     "boundary",
			args:     []string{"--generate", "--case", "boundary", "--duplicate-sequence", "--missing-segment"},
			wantCode: 2,
			mustContain: []string{
				"duplicate_sequence",
				"missing_segment",
				"exit_code=2",
			},
		},
		{
			name:     "resume",
			args:     []string{"--generate", "--case", "damaged", "--resume"},
			wantCode: 2,
			mustContain: []string{
				"resume_index_match=1",
				"resume_hash_match=1",
				"resume_verification=ok",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := exec.Command(binary, tc.args...).CombinedOutput()
			text := string(out)
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatalf("failed to run: %v\n%s", err, text)
			}
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d\n%s", code, tc.wantCode, text)
			}
			for _, want := range tc.mustContain {
				if !strings.Contains(text, want) {
					t.Fatalf("output missing %q\n%s", want, text)
				}
			}
		})
	}
}

func TestBadArgumentsExitOne(t *testing.T) {
	binary := buildTestBinary(t)
	for _, args := range [][]string{
		{"--generate", "--case", "nope"},
		{"--generate", "--format", "avi"},
	} {
		cmd := exec.Command(binary, args...)
		err := cmd.Run()
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
			t.Fatalf("args %v should exit 1, got %v", args, err)
		}
	}
}

func buildTestBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "mediascan")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return binary
}
