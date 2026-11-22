package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/medialab/medialab/internal/clock"
	"github.com/medialab/medialab/internal/render"
	"github.com/medialab/medialab/internal/rtprecover"
)

func frame(index int, ms int64) render.Frame {
	return render.Frame{
		Index: index, TrackID: "v0", ManifestMs: ms,
		Width: render.FrameWidth, Height: render.FrameHeight,
		PixelDigest: uint32(ms), SHA256: string(rune('a'+index)) + "-hash",
		Quality: 1.0,
	}
}

func TestVerifyAcceptsMonotonicAlignedTimeline(t *testing.T) {
	m := Metrics{AlignmentErrMs: 1, PeakQueue: 8}
	frames := []render.Frame{frame(0, 100), frame(1, 200), frame(2, 300)}
	if err := Verify(m, frames); err != nil {
		t.Fatalf("valid run rejected: %v", err)
	}
}

func TestVerifyRejectsInvariants(t *testing.T) {
	base := []render.Frame{frame(0, 100), frame(1, 200)}
	if err := Verify(Metrics{AlignmentErrMs: 2}, base); err == nil {
		t.Fatal("alignment beyond 1ms must fail")
	} else if me, ok := clock.As(err); !ok || me.Kind != clock.KindNonMonotonic {
		t.Fatalf("alignment error kind = %v", err)
	}
	if err := Verify(Metrics{TimelineOverflow: 1}, base); err == nil {
		t.Fatal("time overflow must fail rather than be swallowed")
	}
	bad := []render.Frame{frame(0, 200), frame(1, 200)}
	if err := Verify(Metrics{}, bad); err == nil {
		t.Fatal("non-monotonic frames must fail")
	} else if me, ok := clock.As(err); !ok || me.Kind != clock.KindNonMonotonic {
		t.Fatalf("monotonic error kind = %v", err)
	}
	noHash := frame(0, 1)
	noHash.SHA256 = ""
	if err := Verify(Metrics{}, []render.Frame{noHash}); err == nil {
		t.Fatal("frame without hash must fail")
	}
}

func TestSummaryFieldsAndPercentiles(t *testing.T) {
	m := Metrics{
		TargetBps: 2_500_000, ActualBps: 2_000_000, BitrateDeltaPct: 20,
		Qualities:      []float64{1, 1, 1, 0.85, 0.85, 1},
		AlignmentErrMs: 0, Duplicates: 3, LateDropped: 2, ChecksumBad: 1,
		Gaps: []rtprecover.Gap{{}, {}}, PeakQueue: 8,
	}
	frames := []render.Frame{frame(0, 100), frame(1, 200)}
	s := Build(m, frames)
	if s.Frames != 0 { // Build uses Metrics.Frames explicitly, not the slice length
		t.Fatalf("frames = %d, want metric value 0", s.Frames)
	}
	m.Frames = 2
	s = Build(m, frames)
	if s.QualityMean != 0.95 {
		t.Fatalf("mean = %v, want 0.95", s.QualityMean)
	}
	if s.QualityP95 != 1 {
		t.Fatalf("p95 = %v, want 1", s.QualityP95)
	}
	if s.Gaps != 2 || s.Duplicates != 3 || s.LateDropped != 2 {
		t.Fatalf("counters lost: %+v", s)
	}
	if s.FrameSHA256 == "" {
		t.Fatal("folded frame digest missing")
	}
}

func TestFoldDigestIsOrderSensitiveAndStable(t *testing.T) {
	a := []render.Frame{frame(0, 100), frame(1, 200)}
	b := []render.Frame{frame(1, 200), frame(0, 100)}
	d1, d2 := Build(Metrics{}, a).FrameSHA256, Build(Metrics{}, b).FrameSHA256
	if d1 == d2 {
		t.Fatal("frame reordering must change the folded digest")
	}
	if again := Build(Metrics{}, a).FrameSHA256; again != d1 {
		t.Fatal("repeated build must yield the identical digest")
	}
}

func TestWriteJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "summary.json")
	s := Build(Metrics{TargetBps: 1, ActualBps: 1, Frames: 1, PeakQueue: 4},
		[]render.Frame{frame(0, 1)})
	if err := WriteJSON(path, s); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Summary
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.TargetBps != 1 || got.Frames != 1 || got.FrameSHA256 != s.FrameSHA256 {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	// Contract field names must appear literally for external tooling.
	for _, key := range []string{
		"target_bps", "actual_bps", "bitrate_delta_pct", "frames",
		"quality_mean", "quality_p95", "alignment_error_ms", "duplicates",
		"late_dropped", "gaps", "peak_queue", "frame_sha256",
	} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("summary JSON missing contract field %q", key)
		}
	}
}
