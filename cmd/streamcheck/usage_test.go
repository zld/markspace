package main

import (
	"strings"
	"testing"
)

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"bogus"},
		{"build"},
		{"build", "--input", "/tmp/x", "--rates", "800,800"},
		{"build", "--input", "/tmp/x", "--rates", "800", "--tolerance-ms", "-1"},
		{"build", "--input", "/nonexistent-dir", "--rates", "800"},
		{"fixture"},
		{"fixture", "--out", "/tmp/x", "--segments", "0"},
	} {
		code, _, errOut := runCLI(t, args...)
		if code != exitUsage {
			t.Errorf("run(%v) = %d, want %d (stderr %q)", args, code, exitUsage, errOut)
		}
		if strings.TrimSpace(errOut) == "" {
			t.Errorf("run(%v) produced no error output", args)
		}
	}
}
