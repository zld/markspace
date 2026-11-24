package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/checkpoint"
	"mediad/internal/fixture"
	"mediad/internal/ingest"
)

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func testCfg() Config {
	return Config{
		InputPath: "normal.pcap", SourceRate: 48000, SourceChans: 2,
		TargetRate: 48000, TargetLayout: "stereo", TargetLUFS: -16,
		WindowSize: 64, BurstRun: 3, BlockFrames: 480,
	}
}

func runOnce(t *testing.T, cfg Config, stream []byte, ckpt string, interrupt int) (*Report, []byte) {
	t.Helper()
	var out bytes.Buffer
	r, err := NewRunner(cfg, &out, ckpt, interrupt)
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	rep, err := r.Run(bytes.NewReader(stream))
	if err != nil && !errors.Is(err, ErrInterrupted) {
		t.Fatalf("run: %v", err)
	}
	return rep, out.Bytes()
}

func TestNormalRunStatsAndDeterminism(t *testing.T) {
	stream, _, _ := fixture.Build(fixture.Default())
	cfg := testCfg()
	r1, pcm1 := runOnce(t, cfg, stream, "", 0)
	r2, pcm2 := runOnce(t, cfg, stream, "", 0)

	if r1.Packets == 0 || r1.Duplicates == 0 {
		t.Fatalf("expected packets and injected duplicates, got %+v", r1)
	}
	if r1.SamplesIn == 0 || r1.SamplesOut == 0 {
		t.Fatalf("zero samples: %+v", r1)
	}
	if len(r1.SHA256) != 64 {
		t.Fatalf("sha256 len = %d, want 64", len(r1.SHA256))
	}
	if r1.SHA256 != r2.SHA256 || !bytes.Equal(pcm1, pcm2) {
		t.Fatalf("repeated run produced different output/checksum")
	}
	if int64(len(pcm1)/2) != r1.SamplesOut {
		t.Fatalf("pcm bytes %d != samples %d", len(pcm1), r1.SamplesOut)
	}
}

func TestBurstLossReportsGaps(t *testing.T) {
	p := fixture.Default()
	p.BurstLoss = true
	stream, _, _ := fixture.Build(p)
	rep, _ := runOnce(t, testCfg(), stream, "", 0)
	if rep.Gaps == 0 || rep.Bursts == 0 {
		t.Fatalf("burst loss not reported: %+v", rep)
	}
}

// Cross-subsystem fault 1: ingest corruption must surface with offset and code.
func TestCorruptInputLocated(t *testing.T) {
	stream, wantOff := fixture.Corrupt()
	r, _ := NewRunner(testCfg(), &bytes.Buffer{}, "", 0)
	_, err := r.Run(bytes.NewReader(stream))
	var pe *ingest.ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("want ParseError, got %v", err)
	}
	if pe.Code != ingest.ErrTruncated || pe.Stage != ingest.StageParse {
		t.Fatalf("code/stage = %s/%s", pe.Code, pe.Stage)
	}
	if pe.Offset != wantOff {
		t.Fatalf("offset = %d, want %d (tolerance 1 byte)", pe.Offset, wantOff)
	}
	if diff := pe.Offset - wantOff; diff < -1 || diff > 1 {
		t.Fatalf("offset error exceeds 1 byte: %d", diff)
	}
}

// Cross-subsystem fault 2: interrupted run resumes to an identical checksum.
func TestInterruptResumeIdentical(t *testing.T) {
	dir := t.TempDir()
	stream, _, _ := fixture.Build(fixture.Default())
	cfg := testCfg()
	cfg.InputPath = filepath.Join(dir, "normal.pcap")
	if err := os.WriteFile(cfg.InputPath, stream, 0o644); err != nil {
		t.Fatal(err)
	}
	ckpt := filepath.Join(dir, "job.ckpt")
	outPath := filepath.Join(dir, "out.pcm")

	// Full uninterrupted reference.
	_, fullPCM := runOnce(t, cfg, stream, "", 0)

	// Interrupted first pass writes through a real file.
	f, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(cfg, f, ckpt, 5000) // interrupt after 5000 packets
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(bytes.NewReader(stream))
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("want ErrInterrupted, got %v", err)
	}
	f.Close()
	if _, err := os.Stat(ckpt); err != nil {
		t.Fatalf("checkpoint not written: %v", err)
	}

	// Resume from the committed boundary, appending the suffix.
	out, err := os.OpenFile(outPath, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	resumed, suffix, off, err := Resume(cfg, cfg.InputPath, ckpt, out)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if off <= 0 {
		t.Fatalf("resume offset = %d", off)
	}
	rep, err := resumed.Run(suffix)
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	out.Close()

	finalPCM, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(finalPCM, fullPCM) {
		t.Fatalf("resumed PCM differs from uninterrupted output (%d vs %d bytes)",
			len(finalPCM), len(fullPCM))
	}
	if rep.SHA256 != hashOf(fullPCM) {
		t.Fatalf("resume sha %s != full sha %s", rep.SHA256, hashOf(fullPCM))
	}
	if rep.ResumeMS >= 1000 {
		t.Fatalf("resume_ms = %d, want < 1000", rep.ResumeMS)
	}
}

// Reusing a checkpoint with changed parameters must be rejected (exit 3 class).
func TestResumeConfigMismatchRejected(t *testing.T) {
	dir := t.TempDir()
	stream, _, _ := fixture.Build(fixture.Default())
	cfg := testCfg()
	cfg.InputPath = filepath.Join(dir, "normal.pcap")
	if err := os.WriteFile(cfg.InputPath, stream, 0o644); err != nil {
		t.Fatal(err)
	}
	ckpt := filepath.Join(dir, "job.ckpt")
	outPath := filepath.Join(dir, "out.pcm")
	f, _ := os.Create(outPath)
	runner, _ := NewRunner(cfg, f, ckpt, 500) // enough packets to commit blocks
	if _, err := runner.Run(bytes.NewReader(stream)); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("interrupt: %v", err)
	}
	f.Close()

	changed := cfg
	changed.TargetRate = 24000
	out, _ := os.OpenFile(outPath, os.O_RDWR, 0o644)
	defer out.Close()
	_, _, _, err := Resume(changed, changed.InputPath, ckpt, out)
	var me *checkpoint.MismatchError
	if !errors.As(err, &me) || !errors.Is(err, checkpoint.ErrConfigMismatch) {
		t.Fatalf("want config mismatch, got %v", err)
	}
}
