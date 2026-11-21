package build

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"streamcheck/internal/fixture"
	"streamcheck/internal/verify"
)

func tiers() []fixture.Tier {
	return []fixture.Tier{
		{Name: "450k", BandwidthBPS: 450_000},
		{Name: "900k", BandwidthBPS: 900_000},
		{Name: "1800k", BandwidthBPS: 1_800_000},
	}
}

func baseConfig(t *testing.T) Config {
	return Config{
		OutDir:      filepath.Join(t.TempDir(), "demo"),
		Duration:    36 * time.Second,
		Segment:     3 * time.Second,
		Tiers:       tiers(),
		Concurrency: 3,
		Faults:      map[string]TierFault{},
	}
}

func TestRunHealthyBuild(t *testing.T) {
	cfg := baseConfig(t)
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Report.ValidationFailures != 0 {
		t.Fatalf("clean build failures: %+v", res.Report.Tiers)
	}
	for _, tier := range tiers() {
		if got := res.TierSegments[tier.Name]; got != 12 {
			t.Fatalf("%s segments: %d, want 12", tier.Name, got)
		}
		// Every segment file and both playlists must exist.
		for i := 0; i < 12; i++ {
			if _, err := os.Stat(filepath.Join(cfg.OutDir, tier.Name, segmentName(i))); err != nil {
				t.Fatalf("missing %s: %v", segmentName(i), err)
			}
		}
		if _, err := os.Stat(filepath.Join(cfg.OutDir, tier.Name, "index.m3u8")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.OutDir, "master.m3u8")); err != nil {
		t.Fatalf("master playlist missing: %v", err)
	}
}

func TestRunDropAndSwapFaults(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Faults = map[string]TierFault{
		"900k":  {HasDrop: true, DropSegment: 4},
		"1800k": {HasSwap: true, SwapA: 2, SwapB: 3},
	}
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// One missing-segment failure and one out-of-order failure total.
	if res.Report.ValidationFailures != 2 {
		t.Fatalf("failures: %d (%+v)", res.Report.ValidationFailures,
			res.Report.Tiers)
	}
	counts := map[verify.FailureCode]int{}
	for _, tr := range res.Report.Tiers {
		for _, f := range tr.Failures {
			counts[f.Code]++
		}
	}
	if counts[verify.FailMissingSegment] != 1 {
		t.Fatalf("missing-segment count: %d", counts[verify.FailMissingSegment])
	}
	if counts[verify.FailOutOfOrder] != 1 {
		t.Fatalf("out-of-order count: %d", counts[verify.FailOutOfOrder])
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Duration = cfg.Segment / 2
	if _, err := Run(context.Background(), cfg); err == nil {
		t.Fatal("duration shorter than a segment must error")
	}
	cfg2 := baseConfig(t)
	cfg2.Tiers = nil
	if _, err := Run(context.Background(), cfg2); err == nil {
		t.Fatal("no tiers must error")
	}
}

func TestRunDropIndexOutOfRange(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Faults = map[string]TierFault{
		"450k": {HasDrop: true, DropSegment: 99},
	}
	if _, err := Run(context.Background(), cfg); err == nil {
		t.Fatal("out-of-range drop must error")
	}
}

func TestBitrateScalesWithTier(t *testing.T) {
	cfg := baseConfig(t)
	if _, err := Run(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	sizes := map[string]int64{}
	for _, tier := range tiers() {
		var total int64
		for i := 0; i < 12; i++ {
			info, err := os.Stat(filepath.Join(cfg.OutDir, tier.Name, segmentName(i)))
			if err != nil {
				t.Fatal(err)
			}
			total += info.Size()
		}
		sizes[tier.Name] = total
	}
	if !(sizes["1800k"] > sizes["900k"] && sizes["900k"] > sizes["450k"]) {
		t.Fatalf("segment sizes do not scale with bitrate: %+v", sizes)
	}
}
