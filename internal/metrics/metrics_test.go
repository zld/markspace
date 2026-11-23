package metrics

import (
	"testing"

	"mediainspect/internal/media"
	"mediainspect/internal/normalize"
	"mediainspect/internal/scan"
	"mediainspect/internal/stream"
)

const frameMs = 1000.0 * scan.FrameTicks / media.Timescale

// pipeline runs scan + execute + metrics across modules, returning everything.
func pipeline(t *testing.T, cfg scan.GenConfig, window int, targetKbps, stepMs float64) (
	*scan.Index, *stream.Executor, []stream.OutputFrame, BitrateStats, QualityStats, ManifestStats) {
	t.Helper()
	cfg = cfg.WithDefaults()
	idx, err := scan.BuildIndex(scan.NewReader(scan.NewGenerator(cfg)))
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	ex := stream.NewExecutor(window, cfg.KeyframeInterval, scan.FrameTicks, normalize.DefaultAudioTarget)
	var out []stream.OutputFrame
	r := scan.NewReader(scan.NewGenerator(cfg))
	for {
		f, err := r.Next()
		if err != nil {
			break
		}
		out = append(out, ex.Push(f)...)
	}
	out = append(out, ex.Flush()...)
	var keyMs []float64
	for _, o := range out {
		if o.Frame.Keyframe && !o.Damaged {
			keyMs = append(keyMs, o.TimestampMs)
		}
	}
	br := Bitrate(out, targetKbps, frameMs)
	qs := Quality(out)
	duration := out[len(out)-1].TimestampMs - out[0].TimestampMs + frameMs
	ms := ManifestAlignment(keyMs, stepMs, duration)
	return idx, ex, out, br, qs, ms
}

func TestBitrateDeviationWithinBounds(t *testing.T) {
	cfg := scan.GenConfig{Seed: 42, Frames: 3000, TargetKbps: 1200, LossBurst: 3, ReorderWindow: 8}
	_, _, _, br, _, _ := pipeline(t, cfg, 8, 1200, 1000)
	if br.ActualKbps <= 0 {
		t.Fatal("actual bitrate must be positive")
	}
	if d := br.DeviationPct; d > 5 || d < -5 {
		t.Fatalf("deviation %.2f%% exceeds 5%%", d)
	}
}

func TestManifestAlignment(t *testing.T) {
	cfg := scan.GenConfig{Seed: 42, Frames: 3000, TargetKbps: 1200, LossBurst: 3, ReorderWindow: 8}
	_, _, _, _, _, ms := pipeline(t, cfg, 8, 1200, 1000)
	if ms.Segments == 0 {
		t.Fatal("expected manifest segments")
	}
	if ms.MaxAlignmentMs > 2 {
		t.Fatalf("alignment %.3f ms exceeds 2 ms", ms.MaxAlignmentMs)
	}
}

func TestInvariantsHold(t *testing.T) {
	cfg := scan.GenConfig{Seed: 42, Frames: 3000, TargetKbps: 1200, LossBurst: 3, ReorderWindow: 8}
	idx, ex, _, br, _, _ := pipeline(t, cfg, 8, 1200, 1000)
	// Media duration is timeline-derived (timestamp range), not frame count,
	// so lost frames do not shrink it.
	scanDuration := float64(idx.MaxTimestamp-idx.MinTimestamp)/media.Timescale*1000 + frameMs
	inv := CheckInvariants(idx.Digest, idx.Digest, ex.Stats.InputFrames, ex.Stats,
		scanDuration, br.DurationMs, frameMs)
	if !inv.OK {
		t.Fatalf("invariants violated: %v", inv.Violations)
	}
}

func TestDamagePropagatesAcrossStages(t *testing.T) {
	cfg := scan.GenConfig{Seed: 42, Frames: 3000, TargetKbps: 1200,
		LossBurst: 3, CorruptChecksum: true, TruncateFrame: true,
		BadAudioLayout: true, ColorConflict: true}
	_, ex, out, _, qs, _ := pipeline(t, cfg, 8, 1200, 1000)
	if len(ex.Ranges()) < 2 {
		t.Fatalf("expected multiple corrupt ranges, got %+v", ex.Ranges())
	}
	if qs.CorruptFrames == 0 || qs.CorruptFrames != ex.Stats.DamagedFrames {
		t.Fatalf("quality corrupt=%d damaged=%d", qs.CorruptFrames, ex.Stats.DamagedFrames)
	}
	// Recovery: tail of the stream is valid output again.
	if out[len(out)-1].Damaged {
		t.Fatal("stream must recover after valid keyframes")
	}
	// Metadata errors surfaced per frame reach the aggregate counters.
	if ex.Stats.ErrorsByClass[media.ClassAudioLayout] == 0 ||
		ex.Stats.ErrorsByClass[media.ClassColor] == 0 {
		t.Fatalf("metadata errors lost: %v", ex.Stats.ErrorsByClass)
	}
}

func TestDeterministicRepeat(t *testing.T) {
	cfg := scan.GenConfig{Seed: 42, Frames: 3000, TargetKbps: 1200, LossBurst: 3, ReorderWindow: 8}
	idx1, _, _, br1, qs1, ms1 := pipeline(t, cfg, 8, 1200, 1000)
	idx2, _, _, br2, qs2, ms2 := pipeline(t, cfg, 8, 1200, 1000)
	if idx1.Digest != idx2.Digest {
		t.Fatal("digest not reproducible")
	}
	if br1 != br2 || qs1 != qs2 || ms1 != ms2 {
		t.Fatal("statistics not reproducible across runs")
	}
}
