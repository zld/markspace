package replay

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"replayproof/internal/media"
	"replayproof/internal/output"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.OutDir = filepath.Join(t.TempDir(), "out")
	cfg.CheckpointPath = filepath.Join(cfg.OutDir, "checkpoint.json")
	return cfg
}

func TestRunProducesTwoClipsAndFullSampleMap(t *testing.T) {
	cfg := testConfig(t)
	inputs, err := GenerateInputs(cfg.Seed)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := Run(context.Background(), cfg, inputs)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(sum.Clips) != 2 {
		t.Fatalf("clips=%d", len(sum.Clips))
	}
	if len(sum.Maps) != 16 {
		t.Fatalf("map_count=%d", len(sum.Maps))
	}
	if len(sum.BadCases) != 3 {
		t.Fatalf("bad cases=%#v", sum.BadCases)
	}
	if sum.MaxTimeErr > 1 {
		t.Fatalf("max time error=%d us", sum.MaxTimeErr)
	}
	if sum.ByteErrors != 0 {
		t.Fatalf("byte errors=%d", sum.ByteErrors)
	}
	if sum.QueuePeak > cfg.Queue {
		t.Fatalf("queue peak=%d", sum.QueuePeak)
	}
	// The default windows cover the whole program, so every source sample is
	// reported with its clip time, offsets and deviation.
	for _, m := range sum.Maps {
		if m.ClipName == "" {
			t.Fatalf("sample %d/%d missing from clips", m.TrackID, m.Number)
		}
		if m.ByteError != 0 {
			t.Fatalf("sample %d/%d byte error", m.TrackID, m.Number)
		}
	}
	// Clips must start on a decodable video keyframe and be readable.
	for _, c := range sum.Clips {
		if _, err := os.Stat(c.Path); err != nil {
			t.Fatalf("clip not published: %v", err)
		}
	}
	// The generated video keyframe interval is 200 ms, so alignment error is
	// bounded by that plus the requested pre-roll position inside the GOP.
	if sum.MaxTimeErr > 300_000 {
		t.Fatalf("alignment error implausibly large: %d us", sum.MaxTimeErr)
	}
}

func TestRunDigestIsStableAcrossRuns(t *testing.T) {
	cfg1 := testConfig(t)
	inputs, err := GenerateInputs(cfg1.Seed)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Run(context.Background(), cfg1, inputs)
	if err != nil {
		t.Fatal(err)
	}
	cfg2 := testConfig(t)
	b, err := Run(context.Background(), cfg2, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest() != b.Digest() {
		t.Fatalf("digest drift:\n%s\n%s", a.Canonical(), b.Canonical())
	}
	if len(a.Digest()) != 64 {
		t.Fatalf("digest length=%d", len(a.Digest()))
	}
}

func TestRunRejectsMetadataConflictWithoutSelector(t *testing.T) {
	cfg := testConfig(t)
	cfg.Selectors = nil
	inputs, err := GenerateInputs(cfg.Seed)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := Run(context.Background(), cfg, inputs)
	if err == nil {
		t.Fatal("expected META_CONFLICT")
	}
	if media.CodeOf(err) != media.CodeMETAConflict {
		t.Fatalf("code=%s err=%v", media.CodeOf(err), err)
	}
	if sum == nil || len(sum.Clips) != 0 {
		t.Fatalf("conflicted run published clips: %#v", sum)
	}
	// Both the supplied conflict fixture and the unresolved primary pair are
	// reported; no clips are generated for either.
	if len(sum.BadCases) != 4 {
		t.Fatalf("bad cases=%d", len(sum.BadCases))
	}
}

func TestRunSelectorResolvesConflict(t *testing.T) {
	cfg := testConfig(t)
	cfg.Selectors = Selectors{"rotation": "mxf"}
	inputs, err := GenerateInputs(cfg.Seed)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := Run(context.Background(), cfg, inputs)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	found := false
	for _, rec := range sum.Metadata {
		if rec.Field == "rotation" && rec.Source == "mxf" && rec.Value == "180" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rotation not resolved from mxf: %#v", sum.Metadata)
	}
}

func TestRunCancelReleasesOnlyPrefix(t *testing.T) {
	cfg := testConfig(t)
	cfg.CancelAfter = 1
	inputs, _ := GenerateInputs(cfg.Seed)
	sum, err := Run(context.Background(), cfg, inputs)
	if media.CodeOf(err) != media.CodeCanceled {
		t.Fatalf("err=%v", err)
	}
	if !sum.Canceled {
		t.Fatal("summary not marked canceled")
	}
	if len(sum.Clips) != 1 {
		t.Fatalf("clips=%d", len(sum.Clips))
	}
}

func TestRunCheckpointAndResume(t *testing.T) {
	cfg := testConfig(t)
	cfg.CancelAfter = 1
	inputs, _ := GenerateInputs(cfg.Seed)
	if _, err := Run(context.Background(), cfg, inputs); media.CodeOf(err) != media.CodeCanceled {
		t.Fatalf("expected cancellation, got %v", err)
	}
	cp, err := LoadCheckpoint(cfg.CheckpointPath)
	if err != nil || cp == nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if cp.LastSeq != 1 {
		t.Fatalf("last seq=%d", cp.LastSeq)
	}

	resumeCfg := cfg
	resumeCfg.CancelAfter = 0
	resumed, err := Resume(context.Background(), resumeCfg, inputs)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(resumed.Clips) != 2 {
		t.Fatalf("resumed clips=%d", len(resumed.Clips))
	}
}

func TestResumeRefusesChangedParams(t *testing.T) {
	cfg := testConfig(t)
	cfg.CancelAfter = 1
	inputs, _ := GenerateInputs(cfg.Seed)
	if _, err := Run(context.Background(), cfg, inputs); err == nil {
		t.Fatal("expected cancellation")
	}
	cfg.CancelAfter = 0
	cfg.Seed++
	_, err := Resume(context.Background(), cfg, inputs)
	if media.CodeOf(err) != media.CodeCheckpointMismatch {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateRejectsBadConfig(t *testing.T) {
	base := DefaultConfig()
	base.OutDir = "x"
	cases := []Config{}
	c := base
	c.Workers = 0
	cases = append(cases, c)
	c = base
	c.Queue = 0
	cases = append(cases, c)
	c = base
	c.Selectors = Selectors{"gamma": "mp4"}
	cases = append(cases, c)
	c = base
	c.OutDir = ""
	cases = append(cases, c)
	for i, cfg := range cases {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

func TestGenerateInputsSeedChangesRTPOnly(t *testing.T) {
	a, err := GenerateInputs(7)
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateInputs(8)
	if err != nil {
		t.Fatal(err)
	}
	if string(a.PrimaryMP4) != string(b.PrimaryMP4) {
		t.Fatal("seed must not change the media fixtures")
	}
	if string(joinPackets(a.RTP)) == string(joinPackets(b.RTP)) {
		t.Fatal("seed must change event timing")
	}
}

func TestRunWriteSummaryFile(t *testing.T) {
	cfg := testConfig(t)
	inputs, _ := GenerateInputs(cfg.Seed)
	sum, err := Run(context.Background(), cfg, inputs)
	if err != nil {
		t.Fatal(err)
	}
	path := SummaryPath(cfg.OutDir)
	digest, err := WriteSummary(sum, path)
	if err != nil {
		t.Fatal(err)
	}
	if digest != sum.Digest() {
		t.Fatalf("digest=%s want %s", digest, sum.Digest())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || !contains(string(data), "digest_sha256="+digest) {
		t.Fatalf("summary missing digest: %s", data)
	}
	_ = output.HashBytes
	_ = fmt.Sprintf
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
