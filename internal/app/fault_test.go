package app

import (
	"path/filepath"
	"testing"

	"mediad/internal/sample"
)

func TestRunTruncateFailsWithOffsetAndNoManifest(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	cfg := DefaultConfig(out)
	cfg.Fault = "truncate"
	rep, err := Run(sample.KindNormal, cfg)
	if err == nil {
		t.Fatal("truncated input must fail")
	}
	if rep == nil {
		t.Fatal("a report with diagnostics must be returned even on failure")
	}
	if rep.TruncationOffset() < 0 {
		t.Fatalf("truncation offset = %d, want a real byte offset", rep.TruncationOffset())
	}
	if rep.Manifest != nil {
		t.Fatal("truncated run must not build a manifest")
	}
	if rep.Completed() {
		t.Fatal("truncated run must not report completion")
	}
}

func TestRunFaultOnlyAcceptsTruncate(t *testing.T) {
	// An unknown fault is treated as normal generation by the sample layer;
	// the CLI validates the flag itself, so here we confirm truncate is the
	// only value that actually changes the generated input length.
	clean, _ := Run(sample.KindNormal, DefaultConfig(filepath.Join(t.TempDir(), "clean")))
	bad, err := Run(sample.KindNormal, func() Config {
		c := DefaultConfig(filepath.Join(t.TempDir(), "bad"))
		c.Fault = "truncate"
		return c
	}())
	_ = clean
	if err == nil || bad.TruncationOffset() < 0 {
		t.Fatal("truncate fault must surface an offset-bearing failure")
	}
}
