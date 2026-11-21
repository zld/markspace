package streamcheck_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"streamcheck/internal/build"
	"streamcheck/internal/fixture"
	"streamcheck/internal/m3u8"
	"streamcheck/internal/verify"
)

// TestEndToEndArchiveBuildAndVerify exercises the full data path described in
// the architecture: fixture generation -> TS scan -> clock expansion ->
// keyframe slicing -> playlist write -> verification re-reading the artefacts
// through the same manifest library.
func TestEndToEndArchiveBuildAndVerify(t *testing.T) {
	out := filepath.Join(t.TempDir(), "archive")
	cfg := build.Config{
		OutDir:   out,
		Duration: 36 * time.Second,
		Segment:  3 * time.Second,
		Tiers: []fixture.Tier{
			{Name: "450k", BandwidthBPS: 450_000},
			{Name: "900k", BandwidthBPS: 900_000},
			{Name: "1800k", BandwidthBPS: 1_800_000},
		},
		Concurrency: 3,
		Faults: map[string]build.TierFault{
			"900k":  {HasDrop: true, DropSegment: 4},
			"1800k": {HasSwap: true, SwapA: 2, SwapB: 3},
		},
	}
	result, err := build.Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.ValidationFailures != 2 {
		t.Fatalf("failures: %d", result.Report.ValidationFailures)
	}
	// Every tier lists twelve playlist segments even when a file is missing.
	for _, tr := range result.Report.Tiers {
		if tr.Segments != 12 {
			t.Fatalf("%s segments: %d", tr.Tier, tr.Segments)
		}
	}
	// The maximum duration deviation stays well inside the 50ms budget.
	if result.Report.MaxDeviationMS > 50 {
		t.Fatalf("max deviation %dms exceeds 50ms", result.Report.MaxDeviationMS)
	}
}

// TestEndToEndMasterPlaylistReloads ensures the written master and media
// playlists round-trip through the self-contained m3u8 parser, proving the
// verifier and writer share one data model.
func TestEndToEndMasterPlaylistReloads(t *testing.T) {
	out := filepath.Join(t.TempDir(), "archive")
	cfg := build.Config{
		OutDir:   out,
		Duration: 12 * time.Second,
		Segment:  3 * time.Second,
		Tiers: []fixture.Tier{
			{Name: "450k", BandwidthBPS: 450_000},
			{Name: "900k", BandwidthBPS: 900_000},
		},
		Concurrency: 2,
		Faults:      map[string]build.TierFault{},
	}
	if _, err := build.Run(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	mf, err := os.Open(filepath.Join(out, "master.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	defer mf.Close()
	master, err := m3u8.ParseMaster(mf)
	if err != nil {
		t.Fatal(err)
	}
	if len(master.Variants) != 2 {
		t.Fatalf("variants: %d", len(master.Variants))
	}
	for _, v := range master.Variants {
		mediaPath := filepath.Join(out, filepath.FromSlash(v.URI))
		df, err := os.Open(mediaPath)
		if err != nil {
			t.Fatalf("media playlist %s: %v", v.URI, err)
		}
		pl, err := m3u8.ParseMedia(df)
		df.Close()
		if err != nil {
			t.Fatalf("parse media %s: %v", v.URI, err)
		}
		if len(pl.Segments) != 4 || !pl.Endlist {
			t.Fatalf("media %s segments=%d endlist=%v", v.URI,
				len(pl.Segments), pl.Endlist)
		}
	}
}

// TestEndToEndCleanRunHasNoFailures is the happy path: no faults means the
// verifier reports zero failures across every tier.
func TestEndToEndCleanRunHasNoFailures(t *testing.T) {
	out := filepath.Join(t.TempDir(), "archive")
	cfg := build.Config{
		OutDir:      out,
		Duration:    9 * time.Second,
		Segment:     3 * time.Second,
		Tiers:       []fixture.Tier{{Name: "900k", BandwidthBPS: 900_000}},
		Concurrency: 1,
		Faults:      map[string]build.TierFault{},
	}
	result, err := build.Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range result.Report.Tiers {
		if tr.HasFailures() {
			t.Fatalf("clean run produced failures: %+v", tr.Failures)
		}
	}
	if result.Report.ValidationFailures != 0 {
		t.Fatalf("clean run failures: %d", result.Report.ValidationFailures)
	}
	// Cross-tier check on a single-tier run must not invent failures.
	c := verify.NewChecker(out, 50*time.Millisecond, 0.2)
	if fails := c.CheckCrossTier(result.Report.Tiers); len(fails) != 0 {
		t.Fatalf("single-tier cross check: %+v", fails)
	}
}
