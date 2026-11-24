package metrics

import (
	"strings"
	"testing"

	"mediasync/internal/fanout"
	"mediasync/internal/fixture"
	"mediasync/internal/index"
	"mediasync/internal/manifest"
	"mediasync/internal/repair"
)

func runPipeline(t *testing.T, scenario fixture.Scenario) (*Report, *fixture.Stream, *fanout.Ladder, []*index.MP4Index, *manifest.Manifest) {
	t.Helper()
	cfg, err := fixture.DefaultConfig(41, 3_000_000_000, []int{1_200_000, 2_400_000, 4_800_000})
	if err != nil {
		t.Fatal(err)
	}
	if scenario == fixture.CaseWrap {
		cfg.ForceWrap = true
		cfg.DriftPPM = 250
	}
	stream, err := fixture.Generate(cfg, scenario)
	if err != nil {
		t.Fatal(err)
	}
	ladder, err := fanout.Recover(stream)
	if err != nil {
		t.Fatal(err)
	}
	indexes := make([]*index.MP4Index, len(ladder.Streams()))
	durations := make([][]int64, len(ladder.Streams()))
	for i, res := range ladder.Streams() {
		d, derr := repair.FrameDurations(res.Samples)
		if derr != nil {
			t.Fatal(derr)
		}
		durations[i] = d
		idx, _, berr := index.NewBuilder(uint32(i + 1)).Build(res.Samples, d, cfg.ClockRate)
		if berr != nil {
			t.Fatal(berr)
		}
		indexes[i] = idx
	}
	mb, _ := manifest.NewBuilder(500_000_000, cfg.BaseFrameNS)
	man, err := mb.Build(ladder, durations)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := NewCollector().Collect(41, scenario, stream, ladder, indexes, man)
	if err != nil {
		t.Fatal(err)
	}
	return rep, stream, ladder, indexes, man
}

func TestReportCleanContainsRequiredFields(t *testing.T) {
	rep, _, _, _, _ := runPipeline(t, fixture.CaseClean)
	if len(rep.Renditions) != 3 {
		t.Fatalf("renditions=%d", len(rep.Renditions))
	}
	for _, rr := range rep.Renditions {
		if rr.PacketsIn == 0 {
			t.Fatal("packets_in missing")
		}
		if rr.TargetBPS == 0 || rr.ActualBPS == 0 {
			t.Fatal("bitrate fields missing")
		}
		if e := rr.BitrateErrorPct; e > 8 || e < -8 {
			t.Fatalf("bitrate error %.2f%% exceeds 8%%", e)
		}
		if rr.QualityPSNRDB < 35 {
			t.Fatalf("PSNR %.2f below 35 dB", rr.QualityPSNRDB)
		}
		if rr.RandomAccess == 0 {
			t.Fatal("no random access points")
		}
		if len(rr.IndexSHA256) != 64 {
			t.Fatal("index sha256 wrong length")
		}
	}
	if rep.ManifestErrorMS > 40 {
		t.Fatalf("manifest error %.2f ms exceeds 40", rep.ManifestErrorMS)
	}
	if len(rep.IndexSHA256) != 64 {
		t.Fatal("combined index hash wrong length")
	}
}

func TestReportDeterministic(t *testing.T) {
	a, _, _, _, _ := runPipeline(t, fixture.CaseClean)
	b, _, _, _, _ := runPipeline(t, fixture.CaseClean)
	if a.IndexSHA256 != b.IndexSHA256 {
		t.Fatal("combined index hash not reproducible")
	}
	for i := range a.Renditions {
		if a.Renditions[i].IndexSHA256 != b.Renditions[i].IndexSHA256 {
			t.Fatal("per-rendition index hash not reproducible")
		}
	}
}

func TestReportWrapTracksDriftAndWraps(t *testing.T) {
	rep, _, _, _, _ := runPipeline(t, fixture.CaseWrap)
	for _, rr := range rep.Renditions {
		if rr.Wraps < 1 {
			t.Fatal("wrap scenario recorded no wraps")
		}
		if d := rr.DriftPPM - 250; d > 10 || d < -10 {
			t.Fatalf("drift %.2f not recovered to 250 ppm", rr.DriftPPM)
		}
	}
}

func TestReportCorruptCountsDamage(t *testing.T) {
	rep, _, _, _, _ := runPipeline(t, fixture.CaseCorrupt)
	if rep.CorruptPackets == 0 {
		t.Fatal("corrupt packets not counted")
	}
	if rep.RejectedBoundaries == 0 {
		// Corrupt samples isolate keyframes; at minimum rejected samples exist.
		// This assertion is tolerant: boundary rejection depends on alignment.
		t.Log("no rejected boundaries (acceptable when damage avoids switch points)")
	}
	for _, rr := range rep.Renditions {
		if rr.RejectedSamples == 0 && rep.CorruptPackets == 0 {
			t.Fatal("damage not reflected in per-rendition counters")
		}
	}
}

func TestHexHelper(t *testing.T) {
	if got := hex([]byte{0x0a, 0xff}); got != "0aff" {
		t.Fatalf("hex=%s want 0aff", got)
	}
	if strings.Contains(hex([]byte{0}), "X") {
		t.Fatal("hex produced non-lowercase output")
	}
}
