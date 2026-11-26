package manifest_test

import (
	"errors"
	"testing"

	"continuity/internal/manifest"
	"continuity/internal/media"
)

func goodManifest(c manifest.Constraints) *manifest.Manifest {
	return &manifest.Manifest{
		SchemaVersion: manifest.CanonicalVersion,
		Tracks: []manifest.TrackMeta{
			{ID: 1, Type: "video", SourceBitrateBPS: 3_200_000},
			{ID: 2, Type: "audio", Channels: 2, Layout: "stereo", LayoutKnown: true, InputSampleRate: 48000},
		},
		Constraints: c,
	}
}

func goodReport() media.TimelineReport {
	return media.TimelineReport{EventCount: 90, CoveragePPM: 1_000_000, MaxAVSkewNS: 0}
}

func TestPlanAcceptsGoodTimelineAndIsDeterministic(t *testing.T) {
	c := manifest.Constraints{MinCoveragePPM: 999000, MaxAVSkewMS: 20, MaxBitrateBPS: 2_400_000, BudgetMS: 2000}
	pkg, err := manifest.Plan(manifest.PlanRequest{Manifest: goodManifest(c), Report: goodReport(), ClusterCount: 3})
	if err != nil {
		t.Fatal(err)
	}
	if pkg.CoveragePPM != 1_000_000 || pkg.Bitrate.TargetBPS != 2_200_000 {
		t.Fatalf("coverage=%d bitrate=%d", pkg.CoveragePPM, pkg.Bitrate.TargetBPS)
	}
	if len(pkg.Reasons) == 0 || len(pkg.Audio.Mappings) != 1 {
		t.Fatalf("missing explainability: reasons=%d audio=%+v", len(pkg.Reasons), pkg.Audio)
	}
	s1, err := pkg.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	s2, err := pkg.SHA256()
	if err != nil || s1 != s2 || len(s1) != 64 {
		t.Fatalf("config checksum unstable: %s %s err=%v", s1, s2, err)
	}
}

func TestPlanRejectsCoverageSkewAndInfeasibleBudget(t *testing.T) {
	c := manifest.Constraints{MinCoveragePPM: 999000, MaxAVSkewMS: 20, MaxBitrateBPS: 2_400_000, BudgetMS: 2000}

	badCoverage := goodReport()
	badCoverage.CoveragePPM = 900000
	_, err := manifest.Plan(manifest.PlanRequest{Manifest: goodManifest(c), Report: badCoverage})
	if !errors.Is(err, media.ErrCoverage) {
		t.Fatalf("coverage err=%v", err)
	}

	badSkew := goodReport()
	badSkew.MaxAVSkewNS = 21_000_000
	badSkew.MaxAVSkewMS = 21
	_, err = manifest.Plan(manifest.PlanRequest{Manifest: goodManifest(c), Report: badSkew})
	if !errors.Is(err, media.ErrAVSkew) {
		t.Fatalf("skew err=%v", err)
	}

	tight := c
	tight.BudgetMS = 1
	tight.MaxBitrateBPS = 100_000
	_, err = manifest.Plan(manifest.PlanRequest{Manifest: goodManifest(tight), Report: goodReport(), ClusterCount: 3})
	if !errors.Is(err, media.ErrInfeasible) {
		t.Fatalf("infeasible err=%v", err)
	}
}

func TestPlanRejectsUnknownAudioLayout(t *testing.T) {
	c := manifest.Constraints{MaxBitrateBPS: 2_400_000, BudgetMS: 2000}
	m := goodManifest(c)
	m.Tracks[1].LayoutKnown = false
	m.Tracks[1].Layout = "surprise"
	_, err := manifest.Plan(manifest.PlanRequest{Manifest: m, Report: goodReport()})
	if !errors.Is(err, media.ErrUnknownLayout) {
		t.Fatalf("err=%v", err)
	}
}
