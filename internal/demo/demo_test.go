package demo

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDemoCrashRecoveryAcceptanceFlow(t *testing.T) {
	work := t.TempDir()
	cfg := func(crash int, out *bytes.Buffer) Config {
		return Config{Work: work, Workers: 2, Queue: 2, ChunkBytes: 1 << 20, CrashAfterPublish: crash, Stdout: out}
	}

	var first bytes.Buffer
	err := Run(cfg(1, &first))
	if !errors.Is(err, ErrInjectedCrash) {
		t.Fatalf("first run err=%v want injected crash; output=%s", err, first.String())
	}
	if !strings.Contains(first.String(), "exit=86") {
		t.Fatalf("first output missing 86 marker:\n%s", first.String())
	}

	var second bytes.Buffer
	err = Run(cfg(0, &second))
	if !errors.Is(err, ErrCorruptInputs) {
		t.Fatalf("second run err=%v want corrupt-input exit sentinel", err)
	}
	out := second.String()
	for _, want := range []string{
		"recovery recovered=1 republished=0 missing=0 duplicates=0",
		"roundtrip=100.00%",
		"keyframe_hit=100.00%",
		"segment_sha256=100.00%",
		"peak_chunks=4",
		"class=truncated_moov",
		"class=stco_out_of_bounds",
		"class=backward_timestamp",
		"class=container_only_change",
		"sample_recompute=0",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "republished=1") {
		t.Fatalf("artifact was republished:\n%s", out)
	}
}

func TestDemoNormalRunClassifiesAllFaults(t *testing.T) {
	var out bytes.Buffer
	err := Run(Config{Work: t.TempDir(), Workers: 2, Queue: 2, ChunkBytes: 1 << 20, Stdout: &out})
	if !errors.Is(err, ErrCorruptInputs) {
		t.Fatalf("err=%v", err)
	}
	s := out.String()
	if !strings.Contains(s, "result=corrupt_inputs exit=2") {
		t.Fatalf("missing exit marker:\n%s", s)
	}
	if n := strings.Count(s, "fault class="); n < 3 {
		t.Fatalf("fault lines=%d output:\n%s", n, s)
	}
}

func TestDemoQuarantinesAndRepairsCorruptPublishedArtifact(t *testing.T) {
	work := t.TempDir()
	cfg := Config{Work: work, Workers: 1, Queue: 1, ChunkBytes: 1 << 20, Stdout: &bytes.Buffer{}}
	if err := Run(cfg); !errors.Is(err, ErrCorruptInputs) {
		t.Fatalf("initial run err=%v", err)
	}
	products, err := filepath.Glob(filepath.Join(work, "products", "*.mp4"))
	if err != nil || len(products) != 1 {
		t.Fatalf("products=%v err=%v", products, err)
	}
	if err := os.WriteFile(products[0], []byte("corrupt suffix"), os.ModeAppend|os.ModePerm); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cfg.Stdout = &out
	err = Run(cfg)
	if !errors.Is(err, ErrCorruptInputs) {
		t.Fatalf("repair run err=%v output=%s", err, out.String())
	}
	quarantine, err := filepath.Glob(filepath.Join(work, "products", "quarantine", "*.corrupt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(quarantine) != 1 {
		t.Fatalf("quarantine=%v output=%s", quarantine, out.String())
	}
	if !strings.Contains(out.String(), "roundtrip=100.00%") {
		t.Fatalf("repair did not reverify output:\n%s", out.String())
	}
}
