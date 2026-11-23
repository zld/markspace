package stream

import (
	"testing"

	"mediainspect/internal/media"
	"mediainspect/internal/normalize"
	"mediainspect/internal/scan"
)

func run(t *testing.T, cfg scan.GenConfig, window int) (*Executor, []OutputFrame) {
	t.Helper()
	ex := NewExecutor(window, 30, scan.FrameTicks, normalize.DefaultAudioTarget)
	var out []OutputFrame
	r := scan.NewReader(scan.NewGenerator(cfg))
	for {
		f, err := r.Next()
		if err != nil {
			break
		}
		out = append(out, ex.Push(f)...)
	}
	out = append(out, ex.Flush()...)
	return ex, out
}

func TestCleanStream(t *testing.T) {
	ex, out := run(t, scan.GenConfig{Seed: 42, Frames: 300}, 8)
	if len(ex.Ranges()) != 0 {
		t.Fatalf("clean stream must have no corrupt ranges: %+v", ex.Ranges())
	}
	if ex.Stats.ValidFrames != 300 || ex.Stats.DamagedFrames != 0 {
		t.Fatalf("valid=%d damaged=%d", ex.Stats.ValidFrames, ex.Stats.DamagedFrames)
	}
	// Unified timestamps must be strictly increasing at 33.33 ms steps.
	for i := 1; i < len(out); i++ {
		d := out[i].TimestampMs - out[i-1].TimestampMs
		if d < 33.3 || d > 33.4 {
			t.Fatalf("frame %d: dt=%v ms", i, d)
		}
	}
}

func TestReorderAndDuplicates(t *testing.T) {
	ex, out := run(t, scan.GenConfig{Seed: 42, Frames: 300, ReorderWindow: 8, Duplicates: 5}, 8)
	if ex.Stats.Duplicates != 5 {
		t.Fatalf("duplicates = %d, want 5", ex.Stats.Duplicates)
	}
	if ex.Stats.Recovered == 0 {
		t.Fatal("out-of-order frames must be recovered")
	}
	if ex.Stats.OutputFrames != 300 {
		t.Fatalf("output = %d, want 300 (duplicates counted, not emitted)", ex.Stats.OutputFrames)
	}
	for i := 1; i < len(out); i++ {
		if media.SeqDelta(out[i-1].Frame.Seq, out[i].Frame.Seq) != 1 {
			t.Fatalf("output not in sequence order at %d", i)
		}
	}
}

func TestLateDropOutsideWindow(t *testing.T) {
	// Window of 2 cannot hold a frame shuffled 8 positions back.
	ex, _ := run(t, scan.GenConfig{Seed: 42, Frames: 300, ReorderWindow: 8}, 2)
	if ex.Stats.LateDropped == 0 && ex.Stats.LostFrames == 0 {
		t.Fatal("frames outside the window must be dropped or lost, counted")
	}
}

func TestSeqAndTimestampWrap(t *testing.T) {
	ex, out := run(t, scan.GenConfig{Seed: 5, Frames: 200, Wrap: true}, 8)
	if ex.Stats.ValidFrames != 200 {
		t.Fatalf("valid = %d, want 200 across wrap", ex.Stats.ValidFrames)
	}
	for i := 1; i < len(out); i++ {
		d := out[i].TimestampMs - out[i-1].TimestampMs
		if d < 33.3 || d > 33.4 {
			t.Fatalf("wrap broke monotonic time at %d: dt=%v", i, d)
		}
	}
}

func TestLossBurstDamageAndRecovery(t *testing.T) {
	ex, out := run(t, scan.GenConfig{Seed: 42, Frames: 300, LossBurst: 3}, 8)
	ranges := ex.Ranges()
	if len(ranges) != 1 {
		t.Fatalf("ranges = %d, want 1: %+v", len(ranges), ranges)
	}
	r := ranges[0]
	if r.Reason != "frame_loss" {
		t.Fatalf("reason = %q", r.Reason)
	}
	if r.Frames == 0 || r.EndMs < r.StartMs {
		t.Fatalf("bad range: %+v", r)
	}
	// Recovery: frames after the next valid keyframe are valid again.
	last := out[len(out)-1]
	if last.Damaged {
		t.Fatal("stream must recover after the next valid keyframe")
	}
	if ex.Stats.LostFrames != 3 {
		t.Fatalf("lost = %d, want 3", ex.Stats.LostFrames)
	}
}

func TestChecksumAndTruncationDamage(t *testing.T) {
	ex, _ := run(t, scan.GenConfig{Seed: 42, Frames: 300, CorruptChecksum: true, TruncateFrame: true}, 8)
	if ex.Stats.ErrorsByClass[media.ClassChecksum] != 1 {
		t.Fatalf("checksum errors = %d", ex.Stats.ErrorsByClass[media.ClassChecksum])
	}
	if ex.Stats.ErrorsByClass[media.ClassTruncation] != 1 {
		t.Fatalf("truncation errors = %d", ex.Stats.ErrorsByClass[media.ClassTruncation])
	}
	if len(ex.Ranges()) == 0 {
		t.Fatal("checksum/truncation damage must localize into ranges")
	}
	for _, r := range ex.Ranges() {
		if r.Frames == 0 {
			t.Fatalf("range with no frames: %+v", r)
		}
	}
}

func TestMetadataErrorsPropagate(t *testing.T) {
	ex, _ := run(t, scan.GenConfig{Seed: 42, Frames: 300, BadAudioLayout: true, ColorConflict: true}, 8)
	if ex.Stats.ErrorsByClass[media.ClassAudioLayout] != 2 {
		t.Fatalf("audio layout errors = %d", ex.Stats.ErrorsByClass[media.ClassAudioLayout])
	}
	if ex.Stats.ErrorsByClass[media.ClassSampleRate] != 1 {
		t.Fatalf("sample rate errors = %d", ex.Stats.ErrorsByClass[media.ClassSampleRate])
	}
	if ex.Stats.ErrorsByClass[media.ClassColor] != 1 {
		t.Fatalf("color errors = %d", ex.Stats.ErrorsByClass[media.ClassColor])
	}
	if ex.Stats.AnomalyCount < 2 {
		t.Fatalf("anomalies = %d, want conflict + missing", ex.Stats.AnomalyCount)
	}
}
