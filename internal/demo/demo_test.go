package demo

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"mediapipeline/internal/fixture"
	"mediapipeline/internal/ingest"
)

func runCfg(t *testing.T, cfg Config) *Result {
	t.Helper()
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

func baseCfg(t *testing.T, codec ingest.Codec, inject []string) Config {
	t.Helper()
	return Config{
		Codec: codec, Seed: 17,
		BandwidthBps: 8_000_000,
		Injections:   inject,
		StateDir:     t.TempDir(),
	}
}

func TestCleanRunPassesForEveryCodec(t *testing.T) {
	for _, codec := range []ingest.Codec{ingest.CodecH264, ingest.CodecH265, ingest.CodecAV1} {
		res := runCfg(t, baseCfg(t, codec, nil))
		if res.ExitCode() != 0 || !res.ValidatorPass {
			t.Fatalf("%s: expected pass/exit0, got %+v", codec, res)
		}
		if res.Assets != 3 || res.Missing != 0 || res.Conflicts != 0 {
			t.Fatalf("%s: assets=%d missing=%d conflicts=%d", codec,
				res.Assets, res.Missing, res.Conflicts)
		}
		if len(res.SHA256) != 64 {
			t.Fatalf("%s: bad digest %q", codec, res.SHA256)
		}
	}
}

func TestDamageRunReportsAndRollsBack(t *testing.T) {
	cfg := baseCfg(t, ingest.CodecH265, []string{
		fixture.InjectTruncate, fixture.InjectIndexOOB, fixture.InjectDuplicate,
		fixture.InjectReorder, fixture.InjectBurstLoss, fixture.InjectBandwidthDrop,
	})
	res := runCfg(t, cfg)
	if res.ExitCode() != 2 {
		t.Fatalf("damaged input exit = %d, want 2", res.ExitCode())
	}
	if res.ValidatorPass {
		t.Fatal("damaged input must not validate")
	}
	if res.Gaps != 1 || res.BudgetRejections != 1 {
		t.Fatalf("gaps=%d rejections=%d, want 1/1", res.Gaps, res.BudgetRejections)
	}
	if !res.RolledBack || res.RollbackReason == "" {
		t.Fatal("failed validation must roll back with a reason")
	}
	// Every reported damage location must be within one byte of an injection.
	sample, _ := fixture.Generate(ingest.CodecH265, 17, cfg.Injections)
	for _, d := range res.Damage {
		switch d.Code {
		case "NAL_TRUNCATED", "OBU_TRUNCATED":
			if !withinOne(d.Offset, sample.TruncateOffset) {
				t.Fatalf("truncation at %d vs injected %d", d.Offset, sample.TruncateOffset)
			}
		case "INDEX_OUT_OF_BOUNDS":
			if !withinOne(d.Offset, sample.IndexOOBOffset) {
				t.Fatalf("index OOB at %d vs injected %d", d.Offset, sample.IndexOOBOffset)
			}
		}
	}
}

func TestBudgetRejectionOnCleanInputExitsOne(t *testing.T) {
	cfg := baseCfg(t, ingest.CodecAV1, nil)
	cfg.BandwidthBps = 1 // far too slow for the window, no drop injection
	res := runCfg(t, cfg)
	if res.ExitCode() != 1 {
		t.Fatalf("clean over-budget run exit = %d, want 1", res.ExitCode())
	}
	if res.BudgetRejections != 1 || res.ValidatorPass {
		t.Fatalf("rejections=%d pass=%v", res.BudgetRejections, res.ValidatorPass)
	}
}

func TestResumeDigestMatchesFullRun(t *testing.T) {
	codec := ingest.CodecH264
	full := runCfg(t, baseCfg(t, codec, nil))

	dir := t.TempDir()
	interrupt := Config{Codec: codec, Seed: 17, BandwidthBps: 8_000_000,
		InterruptAfter: 2, StateDir: dir}
	first := runCfg(t, interrupt)
	if !first.InterruptedRun {
		t.Fatalf("first run must interrupt, got %+v", first)
	}
	resume := Config{Codec: codec, Seed: 17, BandwidthBps: 8_000_000,
		Resume: true, StateDir: dir}
	res := runCfg(t, resume)
	if res.ExitCode() != 0 || !res.ValidatorPass {
		t.Fatalf("resume must pass, got %+v", res)
	}
	if res.SHA256 != full.SHA256 {
		t.Fatalf("resume digest %s != full %s", res.SHA256, full.SHA256)
	}
	if res.ElapsedMS < 0 || res.ResumedUnits <= 0 {
		t.Fatalf("resume units=%d elapsed=%d", res.ResumedUnits, res.ElapsedMS)
	}
}

func TestSummaryLineShape(t *testing.T) {
	res := runCfg(t, baseCfg(t, ingest.CodecAV1, nil))
	var buf bytes.Buffer
	WriteSummary(&buf, res)
	line := strings.TrimSpace(buf.String())
	wantPrefix := "assets=3 missing=0 conflicts=0 validator=pass sha256="
	if !strings.HasPrefix(line, wantPrefix) {
		t.Fatalf("summary %q does not start with %q", line, wantPrefix)
	}
	if !strings.HasSuffix(line, res.SHA256) || len(res.SHA256) != 64 {
		t.Fatalf("summary must end with a 64-char sha256, got %q", line)
	}
}

func TestParseInjections(t *testing.T) {
	got := ParseInjections(" a ,,b , ")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("parsed %v", got)
	}
}

func TestBadConfigRejected(t *testing.T) {
	bad := baseCfg(t, ingest.CodecAV1, nil)
	bad.Codec = "mystery"
	if _, err := Run(context.Background(), bad); err == nil {
		t.Fatal("bad codec must fail")
	}
	bad2 := baseCfg(t, ingest.CodecAV1, nil)
	bad2.StateDir = ""
	if _, err := Run(context.Background(), bad2); err == nil {
		t.Fatal("empty state dir must fail")
	}
}

func withinOne(a, b int64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= 1
}
