package metrics

import (
	"fmt"
	"math"
	"testing"

	"mediad/internal/clock"
	mp4 "mediad/internal/mp4"
)

// synthTrack builds a repaired track with fixed cadence for measurement tests.
func synthTrack(originNs int64, n int, cadenceNs int64, startNs int64, size uint32) *mp4.RepairedTrack {
	tr := &mp4.RepairedTrack{TrackID: 1, Kind: "video", Timescale: 90000, OriginNs: originNs}
	for i := 0; i < n; i++ {
		ns := startNs + int64(i)*cadenceNs
		tr.Samples = append(tr.Samples, mp4.RepairedSample{
			Index: uint32(i + 1), Size: size, NS: ns, Sync: i == 0,
			DTS: int64(tr.Timescale), PTS: int64(tr.Timescale),
		})
	}
	tr.FirstNs = originNs
	tr.LastNs = tr.Samples[n-1].NS + originNs
	return tr
}

func TestBitrateExactForSyntheticWindow(t *testing.T) {
	// Two cameras, each delivering 8 s of 30 fps frames of 1000 bytes over a
	// shared wall-clock window. Aggregate is 2*30*8*1000*8 bits / 8 s.
	const fps = 30
	const size = 1000
	cadence := clock.NsPerSecond / fps
	a := synthTrack(0, 30*8, cadence, 0, size)
	b := synthTrack(0, 30*8, cadence, 0, size)
	in := Input{
		TargetBps: 480000,
		Windows: []Window{
			{Camera: "a", StartNs: 0, EndNs: 8 * clock.NsPerSecond},
			{Camera: "b", StartNs: 0, EndNs: 8 * clock.NsPerSecond},
		},
		Tracks: map[string]*mp4.RepairedTrack{"a": a, "b": b},
	}
	r := Compute(in)
	if r.ActualBps != 480000 {
		t.Fatalf("actual bps=%d want 480000", r.ActualBps)
	}
	if math.Abs(r.BitrateDeltaPct) > 1e-9 {
		t.Fatalf("delta=%.6f", r.BitrateDeltaPct)
	}
	if r.Samples != 480 {
		t.Fatalf("samples=%d want 480", r.Samples)
	}
	if r.QualityPSNRDb != 99 {
		t.Fatalf("psnr=%v should hit the finite ceiling", r.QualityPSNRDb)
	}
	if math.IsNaN(r.QualityPSNRDb) || math.IsInf(r.QualityPSNRDb, 0) {
		t.Fatal("quality must be finite")
	}
	// The exclusive end at 8 s falls exactly on the next frame, so the last
	// delivered frame is one cadence earlier; the residual is one cadence,
	// which is the convention (nearest boundary is the exclusive end itself).
	if r.AlignmentErrorMs > 34 {
		t.Fatalf("alignment=%.3fms for perfectly gridded delivery", r.AlignmentErrorMs)
	}
}

func TestAlignmentReportsGridResidual(t *testing.T) {
	const cadence = int64(33_333_333)
	tr := synthTrack(0, 300, cadence, 0, 100)
	// Window starts half a frame late relative to the delivered grid; the
	// residual must be 16.67 ms, not the full frame.
	in := Input{
		TargetBps: 1,
		Windows:   []Window{{Camera: "a", StartNs: cadence / 2, EndNs: 10 * clock.NsPerSecond}},
		Tracks:    map[string]*mp4.RepairedTrack{"a": tr},
	}
	r := Compute(in)
	got := r.AlignmentErrorMs
	if got < 16.0 || got > 17.0 {
		t.Fatalf("alignment=%.3fms want ~16.67", got)
	}
}

func TestQualityFiniteOnIrregularGrid(t *testing.T) {
	tr := &mp4.RepairedTrack{OriginNs: 0, Timescale: 90000}
	// One sample jumps by a whole frame; PSNR must degrade but stay finite.
	ns := int64(0)
	for i := 0; i < 20; i++ {
		step := int64(33_333_333)
		if i == 10 {
			step *= 3
		}
		ns += step
		tr.Samples = append(tr.Samples, mp4.RepairedSample{Index: uint32(i + 1), NS: ns, Size: 10})
	}
	tr.FirstNs, tr.LastNs = 0, ns
	in := Input{TargetBps: 1, Windows: []Window{{Camera: "a", StartNs: 0, EndNs: ns}}, Tracks: map[string]*mp4.RepairedTrack{"a": tr}}
	r := Compute(in)
	if r.QualityPSNRDb <= 0 || math.IsInf(r.QualityPSNRDb, 0) || math.IsNaN(r.QualityPSNRDb) {
		t.Fatalf("psnr=%v", r.QualityPSNRDb)
	}
	if r.QualityPSNRDb >= 99 {
		t.Fatalf("irregular grid should not hit the ceiling: %v", r.QualityPSNRDb)
	}
}

func TestWindowUnion(t *testing.T) {
	ws := []Window{
		{Camera: "a", StartNs: 0, EndNs: 10 * clock.NsPerSecond},
		{Camera: "b", StartNs: 5 * clock.NsPerSecond, EndNs: 15 * clock.NsPerSecond},
		{Camera: "c", StartNs: 20 * clock.NsPerSecond, EndNs: 25 * clock.NsPerSecond},
	}
	if got := windowUnionNs(ws); got != 20*clock.NsPerSecond {
		t.Fatalf("union=%d want 20s", got)
	}
}

func TestEmptyInputIsSafe(t *testing.T) {
	r := Compute(Input{TargetBps: 8_000_000})
	if r.ActualBps != 0 || r.Coverage() != 0 {
		t.Fatalf("unexpected: %+v", r)
	}
}

func TestNumbersStayFiniteAcrossExtremes(t *testing.T) {
	tr := synthTrack(0, 2, 1, 0, 1)
	in := Input{TargetBps: 0, Windows: []Window{{Camera: "a", StartNs: 0, EndNs: 1}}, Tracks: map[string]*mp4.RepairedTrack{"a": tr}}
	r := Compute(in)
	for name, v := range map[string]float64{
		"delta": r.BitrateDeltaPct, "psnr": r.QualityPSNRDb, "align": r.AlignmentErrorMs, "cov": r.IndexCoveragePct,
	} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatalf("%s not finite: %v", name, v)
		}
	}
	_ = fmt.Sprint(r)
}

// Coverage is a helper used only by tests.
func (r *Report) Coverage() float64 { return r.IndexCoveragePct }
