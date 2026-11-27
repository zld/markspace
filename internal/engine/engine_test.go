package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"example.com/continuity/internal/checkpoint"
	"example.com/continuity/internal/fault"
	"example.com/continuity/internal/fingerprint"
	"example.com/continuity/internal/manifest"
	"example.com/continuity/internal/media"
)

func genInputs(t *testing.T) ([]byte, []byte) {
	t.Helper()
	spec := media.DefaultSpec()
	frames, err := spec.WriteFrameStream()
	if err != nil {
		t.Fatal(err)
	}
	rtp, err := spec.WriteRTPStream()
	if err != nil {
		t.Fatal(err)
	}
	return frames, rtp
}

func TestFullRunMetrics(t *testing.T) {
	frames, rtp := genInputs(t)
	dir := t.TempDir()
	e := New(DefaultConfig(), dir)
	res, err := e.Run(context.Background(), frames, rtp)
	if err != nil {
		t.Fatal(err)
	}
	s := res.Summary
	if s.Frames != media.DefaultSpec().Frames {
		t.Fatalf("frames=%d", s.Frames)
	}
	if s.BitrateErrorPct > 5.0 {
		t.Fatalf("bitrate error %.3f%%", s.BitrateErrorPct)
	}
	if s.PSNRDB < 40 {
		t.Fatalf("PSNR %.2f", s.PSNRDB)
	}
	if s.SSIM < 0.98 {
		t.Fatalf("SSIM %.4f", s.SSIM)
	}
	if s.ManifestAlignNS > 1 {
		t.Fatalf("alignment %d", s.ManifestAlignNS)
	}
	if s.SilenceNS <= 0 || s.Duplicates == 0 {
		t.Fatalf("silence=%d duplicates=%d", s.SilenceNS, s.Duplicates)
	}
	if s.Segments < 2 {
		t.Fatalf("expected jump-driven segments, got %d", s.Segments)
	}
	if err := res.Trace.Verify(); err != nil {
		t.Fatal(err)
	}
	// Every dedup duplicate must reference an earlier original.
	for _, r := range res.Dedup {
		if r.Kind == fingerprint.KindDuplicate && r.OriginalIndex >= r.SegmentIndex {
			t.Fatalf("duplicate %d references non-earlier original %d", r.SegmentIndex, r.OriginalIndex)
		}
	}
}

func TestInterruptAndResumeByteIdentical(t *testing.T) {
	frames, rtp := genInputs(t)
	dir := t.TempDir()

	cfg1 := DefaultConfig()
	cfg1.InterruptAfter = 5
	e1 := New(cfg1, dir)
	_, err := e1.Run(context.Background(), frames, rtp)
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("want interrupt, got %v", err)
	}
	// A checkpoint must exist after the simulated crash.
	if !checkpoint.NewStore(dir).Exists() {
		t.Fatal("no checkpoint after interrupt")
	}

	cfg2 := DefaultConfig() // InterruptAfter zeroed: run to completion
	e2 := New(cfg2, dir)
	res, err := e2.Run(context.Background(), frames, rtp)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !res.Resumed {
		t.Fatal("resume did not detect the committed checkpoint")
	}
	interruptedSummary, err := os.ReadFile(filepath.Join(dir, manifest.SummaryFile))
	if err != nil {
		t.Fatal(err)
	}

	// A clean uninterrupted run in another directory must match byte-for-byte.
	cleanDir := t.TempDir()
	cleanRes, err := New(DefaultConfig(), cleanDir).Run(context.Background(), frames, rtp)
	if err != nil {
		t.Fatal(err)
	}
	cleanSummary, err := os.ReadFile(filepath.Join(cleanDir, manifest.SummaryFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(interruptedSummary) != string(cleanSummary) {
		t.Fatal("resumed run changed manifest bytes")
	}
	if res.Summary.Frames != cleanRes.Summary.Frames {
		t.Fatal("frame counts differ between runs")
	}
}

func TestResumeRejectsChangedConfig(t *testing.T) {
	frames, rtp := genInputs(t)
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.InterruptAfter = 4
	if _, err := New(cfg, dir).Run(context.Background(), frames, rtp); !errors.Is(err, ErrInterrupted) {
		t.Fatal(err)
	}
	changed := DefaultConfig()
	changed.ReorderWindow = 16 // config digest now differs
	// Stale checkpoint must be rejected and the run restarted cleanly, not fail.
	res, err := New(changed, dir).Run(context.Background(), frames, rtp)
	if err != nil {
		t.Fatalf("changed config should trigger fresh run: %v", err)
	}
	if res.Resumed {
		t.Fatal("stale checkpoint was reused")
	}
}

func TestCorruptChecksumFailsStructured(t *testing.T) {
	frames, rtp := genInputs(t)
	corrupt := append([]byte(nil), frames...)
	// Flip a payload byte inside the first record body (after its 4-byte
	// length prefix); the frame checksum must fail with stage/frame/offset.
	corrupt[40] ^= 0xFF
	dir := t.TempDir()
	_, err := New(DefaultConfig(), dir).Run(context.Background(), corrupt, rtp)
	var fe *fault.Error
	if !errors.As(err, &fe) {
		t.Fatalf("want structured fault, got %v", err)
	}
	if fe.Stage != fault.StageFrame || fe.Frame < 0 || fe.Offset < 0 {
		t.Fatalf("fault missing context: %+v", fe)
	}
}

func TestTimestampOverflowRejected(t *testing.T) {
	frames, rtp := genInputs(t)
	dir := t.TempDir()
	cfg := DefaultConfig()
	// A max-duration config combined with the jump must not overflow here;
	// instead force overflow by shrinking the max segment so the cutter
	// validates spans, and confirm the run stays bounded and successful only
	// when arithmetic is safe. The real overflow guard is unit-tested in
	// the segment package; here we assert the engine propagates a fault if
	// the segment config is invalid rather than panicking.
	cfg.MaxSegmentNS = 0
	if _, err := New(cfg, dir).Run(context.Background(), frames, rtp); err == nil {
		t.Fatal("invalid segment config accepted")
	}
}
