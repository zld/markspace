package job

import (
	"os"
	"path/filepath"
	"testing"

	"archive-recovery/internal/adapter"
	"archive-recovery/internal/media"
)

// minimalAV1Stream builds a deterministic AV1 low-overhead stream with the
// requested number of frames; every third frame is a key frame.
func minimalAV1Stream(t *testing.T, frames int, truncateLast bool) []byte {
	t.Helper()
	var out []byte
	put := func(obuType int, payload []byte) {
		h := byte(obuType)<<3 | 0x02
		out = append(out, h)
		v := len(payload)
		for {
			b := byte(v & 0x7f)
			v >>= 7
			if v != 0 {
				out = append(out, b|0x80)
			} else {
				out = append(out, b)
				break
			}
		}
		out = append(out, payload...)
	}
	put(adapter.OBUTypeSeqHeaderForTest(), []byte{0x01})
	for i := 0; i < frames; i++ {
		payload := []byte{0x40, 0x11} // INTER frame by default
		if i%3 == 0 {
			payload[0] = 0x00 // key frame (frame_type=0)
		}
		put(adapter.OBUTypeFrameForTest(), payload)
	}
	if truncateLast {
		out = append(out, byte(adapter.OBUTypeFrameForTest())<<3|0x02, 40, 1, 2)
	}
	return out
}

func regularHints(n int) []PTSHint {
	h := make([]PTSHint, n)
	for i := range h {
		h[i] = PTSHint{PTS: int64(i) * 3000, Has: true}
	}
	return h
}

func TestRunFullThenIdempotent(t *testing.T) {
	dir := t.TempDir()
	stream := minimalAV1Stream(t, 12, false)
	in := JobInput{Codec: "av1", ChunkBytes: 256, Seed: 7,
		Stream: stream, PTS: regularHints(12), WorkDir: dir}
	r1, err := Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Status != StatusComplete || r1.SHA256Match != r1.Chunks {
		t.Fatalf("healthy stream must fully match: %+v", r1)
	}
	if r1.RoundtripMatchPct != 100 || r1.KeyframeHitPct != 100 {
		t.Fatalf("percentages wrong: rt=%.2f key=%.2f", r1.RoundtripMatchPct, r1.KeyframeHitPct)
	}
	// Re-run without resume flag must overwrite and yield the same manifest.
	r2, err := Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if r2.ManifestSHA256 != r1.ManifestSHA256 {
		t.Fatalf("repeated run changed manifest: %s vs %s", r1.ManifestSHA256, r2.ManifestSHA256)
	}
}

func TestInterruptAndResumePropagation(t *testing.T) {
	dir := t.TempDir()
	stream := minimalAV1Stream(t, 18, false)
	in := JobInput{Codec: "av1", ChunkBytes: 32, Seed: 11,
		Stream: stream, PTS: regularHints(18), InterruptAfter: 2, WorkDir: dir}
	ri, err := Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if ri.Status != StatusInterrupted || ri.Chunks != 2 {
		t.Fatalf("interrupt state wrong: %+v", ri)
	}
	if _, err := os.Stat(filepath.Join(dir, "checkpoint.json")); err != nil {
		t.Fatalf("checkpoint not persisted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("manifest must not exist before completion")
	}

	// Resume: same input/config, checkpoint validated, chunks skipped.
	in2 := in
	in2.InterruptAfter = 0
	in2.Resume = true
	r2, err := Run(in2)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Status != StatusComplete || r2.ResumeSkippedChunks != 2 {
		t.Fatalf("resume result wrong: %+v", r2)
	}
	if r2.RoundtripMatchPct != 100 || r2.KeyframeHitPct != 100 {
		t.Fatalf("resume must keep full correctness: %+v", r2)
	}

	// A third, non-resume run reproduces the identical manifest.
	r3, err := Run(JobInput{Codec: "av1", ChunkBytes: 32, Seed: 11,
		Stream: stream, PTS: regularHints(18), WorkDir: dir})
	if err != nil || r3.ManifestSHA256 != r2.ManifestSHA256 {
		t.Fatalf("determinism across runs broken: %v %s %s", err, r3.ManifestSHA256, r2.ManifestSHA256)
	}
}

func TestCheckpointRejectsChangedInputAndConfig(t *testing.T) {
	dir := t.TempDir()
	stream := minimalAV1Stream(t, 9, false)
	base := JobInput{Codec: "av1", ChunkBytes: 32, Seed: 5,
		Stream: stream, PTS: regularHints(9), InterruptAfter: 1, WorkDir: dir}
	if _, err := Run(base); err != nil {
		t.Fatal(err)
	}

	// Changed input bytes.
	tampered := append([]byte{}, stream...)
	tampered[len(tampered)-1] ^= 0xFF
	badInput := base
	badInput.Stream = tampered
	badInput.InterruptAfter = 0
	badInput.Resume = true
	if _, err := Run(badInput); media.ClassOf(err) != media.ClassCheckpoint {
		t.Fatalf("changed input must be refused, got %v", err)
	}

	// Changed chunk size -> config digest changes.
	badCfg := base
	badCfg.ChunkBytes = 64
	badCfg.InterruptAfter = 0
	badCfg.Resume = true
	if _, err := Run(badCfg); media.ClassOf(err) != media.ClassCheckpoint {
		t.Fatalf("changed config must be refused, got %v", err)
	}

	// Resume without any checkpoint.
	empty := t.TempDir()
	if _, err := Run(JobInput{Codec: "av1", ChunkBytes: 32, Seed: 5,
		Stream: stream, Resume: true, WorkDir: empty}); media.ClassOf(err) != media.ClassCheckpoint {
		t.Fatalf("missing checkpoint must fail, got %v", err)
	}
}

func TestTruncatedUnitProducesRecoverableAndMissingIndexEntry(t *testing.T) {
	dir := t.TempDir()
	// 9 complete frames plus one dangling truncated OBU.
	stream := minimalAV1Stream(t, 9, true)
	r, err := Run(JobInput{Codec: "av1", ChunkBytes: 16, Seed: 3,
		Stream: stream, PTS: regularHints(9), WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !r.RecoverableError || r.TruncatedUnits != 1 {
		t.Fatalf("want one truncated unit, got %+v", r)
	}
	if r.FramesTotal != 9 {
		t.Fatalf("truncated unit must not become an index entry, got %d frames", r.FramesTotal)
	}
	if r.RoundtripMatchPct >= 100 {
		t.Fatalf("tainted tail must lower roundtrip below 100, got %.2f", r.RoundtripMatchPct)
	}
	if r.SHA256Match >= r.Chunks {
		t.Fatalf("tainted chunk must not count as matched: %d/%d", r.SHA256Match, r.Chunks)
	}
}
