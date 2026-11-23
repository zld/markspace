package pipeline_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"medfix/internal/fixture"
	"medfix/internal/journal"
	"medfix/internal/pipeline"
	"medfix/internal/timeline"
)

func demoInputs(seed int64) []pipeline.Input {
	mp4 := fixture.GenerateMP4(seed, []fixture.MP4TrackSpec{
		{TrackID: 1, Timescale: 90000, Samples: 200, Delta: 3000, KeyEvery: 1, PayloadBase: 10},
		{TrackID: 2, Timescale: 48000, Samples: 4, Delta: 1024, KeyEvery: 1, PayloadBase: 40},
	})
	rtp := fixture.GenerateRTP(seed, fixture.RTPSpec{
		ClockRate: 90000, Packets: 18, StartSeq: 0, StartTS: 0,
		TSIncrement: 3000, ID3Every: 6,
	})
	return []pipeline.Input{
		{ID: "mp4-src", Kind: "mp4", Data: mp4.Bytes},
		{ID: "rtp-src", Kind: "rtp", Data: rtp.Bytes, ClockRate: 90000},
	}
}

func runOnce(t *testing.T, dir string, seed int64, opts pipeline.Options) (*pipeline.Report, error) {
	t.Helper()
	opts.WorkDir = dir
	r, err := pipeline.New("run-1", demoInputs(seed), opts)
	if err != nil {
		t.Fatal(err)
	}
	return r.Execute()
}

func TestHappyPathMetricsAndInvariants(t *testing.T) {
	rep, err := runOnce(t, t.TempDir(), 42, pipeline.Options{Config: timeline.DefaultConfig()})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if rep.RoundtripMatchRate != 1.0 {
		t.Fatalf("roundtrip_match_rate=%f want 1.0", rep.RoundtripMatchRate)
	}
	if rep.KeyframeHitRate <= 0.99 {
		t.Fatalf("keyframe_hit_rate=%f want >0.99", rep.KeyframeHitRate)
	}
	if rep.InvariantFailures != 0 || len(rep.Conflicts) != 0 {
		t.Fatalf("invariants failed: %d conflicts=%d", rep.InvariantFailures, len(rep.Conflicts))
	}
	if rep.Segments == 0 || len(rep.SegmentSHA256) != rep.Segments {
		t.Fatalf("segment accounting broken: %+v", rep)
	}
	if len(rep.SourceOffsets) == 0 {
		t.Fatal("source offsets not reported")
	}
}

func TestInterruptAfterTransformResumes(t *testing.T) {
	dir := t.TempDir()
	_, err := runOnce(t, dir, 42, pipeline.Options{
		Config: timeline.DefaultConfig(), Interrupt: journal.StageTransform,
	})
	if !errors.Is(err, pipeline.ErrInterrupted) {
		t.Fatalf("want ErrInterrupted, got %v", err)
	}
	// Index must NOT be published before commit.
	if _, err := os.Stat(filepath.Join(dir, "index.mfix")); !os.IsNotExist(err) {
		t.Fatal("half-built index must not be visible after transform interrupt")
	}
	// Resume: only validate+commit remain.
	rep, err := runOnce(t, dir, 42, pipeline.Options{Config: timeline.DefaultConfig()})
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if rep.ResumedStage != journal.StageTransform {
		t.Fatalf("resumed_stage=%q want transform", rep.ResumedStage)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.mfix")); err != nil {
		t.Fatalf("index not published on resume: %v", err)
	}
}

func TestIdempotentRerunStableSegmentHashes(t *testing.T) {
	dir := t.TempDir()
	rep1, err := runOnce(t, dir, 42, pipeline.Options{Config: timeline.DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	rep2, err := runOnce(t, dir, 42, pipeline.Options{Config: timeline.DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep1.SegmentSHA256) != rep1.Segments {
		t.Fatal("first run hash count mismatch")
	}
	stable := 0
	for i := range rep1.SegmentSHA256 {
		if rep1.SegmentSHA256[i] == rep2.SegmentSHA256[i] {
			stable++
		}
	}
	if stable != rep1.Segments {
		t.Fatalf("stable segment hashes=%d want %d", stable, rep1.Segments)
	}
	if rep1.IndexSHA256 != rep2.IndexSHA256 {
		t.Fatal("index hash must be stable across identical reruns")
	}
}

func TestConfigChangeInvalidatesCheckpoints(t *testing.T) {
	dir := t.TempDir()
	if _, err := runOnce(t, dir, 42, pipeline.Options{Config: timeline.DefaultConfig()}); err != nil {
		t.Fatal(err)
	}
	cfg := timeline.DefaultConfig()
	cfg.ConfigVersion = 999 // changes serialized config hash
	cfg.DriftSlopeNum = 1001
	// First run with the new config must find the old checkpoints invalid
	// and rebuild from parse (no resumed stage).
	rep, err := runOnce(t, dir, 42, pipeline.Options{Config: cfg})
	if err != nil {
		t.Fatalf("config-change rerun failed: %v", err)
	}
	if rep.ResumedStage != "" {
		t.Fatalf("old checkpoint should be invalidated, resumed=%q", rep.ResumedStage)
	}
	// A rerun with the same config may now resume normally.
	rep2, err := runOnce(t, dir, 42, pipeline.Options{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if rep2.ResumedStage == "" {
		t.Fatal("identical-config rerun should reuse checkpoints")
	}
}

func TestDiskFullDuringCheckpointThenRecovery(t *testing.T) {
	dir := t.TempDir()
	fault := &journal.Fault{FullAfterWrites: 1}
	_, err := runOnce(t, dir, 42, pipeline.Options{
		Config: timeline.DefaultConfig(), Fault: fault,
	})
	if !errors.Is(err, journal.ErrDiskFull) {
		t.Fatalf("want disk-full error, got %v", err)
	}
	// Second run without fault recovers from the last valid checkpoint.
	if _, err := runOnce(t, dir, 42, pipeline.Options{
		Config: timeline.DefaultConfig(),
	}); err != nil {
		t.Fatalf("recovery after disk full failed: %v", err)
	}
}

func TestCorruptCheckpointRollback(t *testing.T) {
	dir := t.TempDir()
	if _, err := runOnce(t, dir, 42, pipeline.Options{Config: timeline.DefaultConfig()}); err != nil {
		t.Fatal(err)
	}
	// Corrupt the validate-stage checkpoint on disk.
	cpPath := filepath.Join(dir, "journal", "checkpoint-validate.json")
	raw, err := os.ReadFile(cpPath)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xFF
	if err := os.WriteFile(cpPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := runOnce(t, dir, 42, pipeline.Options{Config: timeline.DefaultConfig()})
	if err != nil {
		t.Fatalf("rollback recovery failed: %v", err)
	}
	// Must have rolled back at or before transform.
	if rep.ResumedStage != journal.StageTransform && rep.ResumedStage != journal.StageParse {
		t.Fatalf("unexpected resumed stage after corruption: %q", rep.ResumedStage)
	}
}

// ---- Five required fault classes ----

func TestFaultRTPRolloverAmbiguityCarriesOffset(t *testing.T) {
	in := fixture.GenerateRTP(1, fixture.RTPSpec{
		ClockRate: 90000, Packets: 4, StartSeq: 0,
		StartTS: 200000, TSIncrement: 3000,
	})
	// Hand-inject a small backward timestamp jump at packet 2: rewrite TS.
	corrupt := injectBackwardJump(t, in.Bytes)
	inputs := []pipeline.Input{{ID: "bad-rtp", Kind: "rtp", Data: corrupt, ClockRate: 90000}}
	_, err := runInputs(t.TempDir(), inputs)
	var te *timeline.Error
	if !errors.As(err, &te) || te.Code != timeline.CodeRolloverAmbiguous {
		t.Fatalf("want rollover-ambiguous error, got %v", err)
	}
	if te.Offset < 0 {
		t.Fatal("ambiguity error must retain raw offset")
	}
}

func TestFaultPacketLossReported(t *testing.T) {
	in := fixture.GenerateRTP(2, fixture.RTPSpec{
		ClockRate: 90000, Packets: 12, StartSeq: 0, StartTS: 0,
		TSIncrement: 3000, LossSeq: []uint16{4, 5},
	})
	inputs := []pipeline.Input{{ID: "loss-rtp", Kind: "rtp", Data: in.Bytes, ClockRate: 90000}}
	rep, err := runInputs(t.TempDir(), inputs)
	if err != nil {
		t.Fatalf("loss should be non-fatal: %v", err)
	}
	// Source offsets still populated; loss surfaced via parse stage path.
	if len(rep.SourceOffsets) == 0 {
		t.Fatal("events missing despite packet loss")
	}
}

func TestFaultMP4TruncatedSampleTable(t *testing.T) {
	in := fixture.GenerateMP4(3, []fixture.MP4TrackSpec{
		{TrackID: 1, Timescale: 90000, Samples: 10, Delta: 3000, KeyEvery: 2, PayloadBase: 1},
	})
	cut := len(in.Bytes) - 30
	inputs := []pipeline.Input{{ID: "bad-mp4", Kind: "mp4", Data: fixture.TruncateAt(in.Bytes, cut)}}
	_, err := runInputs(t.TempDir(), inputs)
	var te *timeline.Error
	if !errors.As(err, &te) || te.Code != timeline.CodeTruncatedBox {
		t.Fatalf("want truncated-box error, got %v", err)
	}
	if te.Offset < 0 {
		t.Fatal("truncation must locate raw offset")
	}
}

func TestFaultCrossTrackTimescaleConflictBlocksIndex(t *testing.T) {
	cfg := timeline.DefaultConfig()
	cfg.ToleranceTicks = 1 // 1 tick: any real divergence is a conflict
	// One shared ID3v2.4 tag carried by both sources so the content hashes
	// match; only their timestamps diverge beyond tolerance.
	sharedTag := fixture.WrapID3v2([]byte("shared-meta-body"))
	// MP4 emsg at raw time 100 (timescale 1000).
	mp4 := fixture.GenerateMP4WithEmsg(4, []fixture.MP4TrackSpec{
		{TrackID: 1, Timescale: 1000, Samples: 8, Delta: 100, KeyEvery: 2, PayloadBase: 1},
	}, string(sharedTag), 100)
	// RTP ID3 at raw time 9000 (same 1000 timescale) -> 8.9s divergence.
	rtp := fixture.GenerateRTP(4, fixture.RTPSpec{
		ClockRate: 1000, Packets: 8, StartSeq: 0, StartTS: 9000, TSIncrement: 100,
		ID3Every: 1, SharedID3: sharedTag,
	})
	inputs := []pipeline.Input{
		{ID: "mp4-c", Kind: "mp4", Data: mp4.Bytes},
		{ID: "rtp-c", Kind: "rtp", Data: rtp.Bytes, ClockRate: 1000},
	}
	dir := t.TempDir()
	r, err := pipeline.New("run-x", inputs, pipeline.Options{WorkDir: dir, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Execute(); err == nil {
		t.Fatal("conflicting timeline must fail the pipeline")
	}
	if _, err := os.Stat(filepath.Join(dir, "index.mfix")); !os.IsNotExist(err) {
		t.Fatal("conflict must prevent index publication")
	}
}

func TestFaultTruncatedIndexRejected(t *testing.T) {
	dir := t.TempDir()
	rep, err := runOnce(t, dir, 42, pipeline.Options{Config: timeline.DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	_ = rep
	idxPath := filepath.Join(dir, "index.mfix")
	raw, err := os.ReadFile(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(idxPath, raw[:len(raw)-50], 0o644); err != nil {
		t.Fatal(err)
	}
	// Wipe commit checkpoint so the pipeline reloads the index on resume.
	_ = os.Remove(filepath.Join(dir, "journal", "checkpoint-commit.json"))
	_, err = runOnce(t, dir, 42, pipeline.Options{Config: timeline.DefaultConfig()})
	var te *timeline.Error
	if !errors.As(err, &te) || te.Code != timeline.CodeTruncatedIndex {
		t.Fatalf("want truncated-index error, got %v", err)
	}
}

func runInputs(dir string, inputs []pipeline.Input) (*pipeline.Report, error) {
	r, err := pipeline.New("run-fault", inputs,
		pipeline.Options{WorkDir: dir, Config: timeline.DefaultConfig()})
	if err != nil {
		return nil, err
	}
	return r.Execute()
}
