package pipeline

import (
	"context"
	"math"
	"testing"

	"mediaarchive/internal/fixtures"
)

// TestDriftFixtureIsDetected proves the whole chain (generator -> MP4 table ->
// keyframe cadence -> manifest metric) surfaces sustained clock drift rather
// than averaging it away.
func TestDriftFixtureIsDetected(t *testing.T) {
	dir := t.TempDir()
	set, err := fixtures.Generate(dir, fixtures.SetDrift)
	if err != nil {
		t.Fatal(err)
	}
	var assets []AssetInput
	for _, a := range set.Assets {
		assets = append(assets, AssetInput{Asset: a.Name, MP4: a.MP4, Subtitle: a.Subtitle, FPS: "30000/1001", TargetBps: a.TargetBps})
	}
	m, err := Run(context.Background(), Options{Mode: ModeRecover, Workers: 2, Queue: 2, FPS: "30000/1001"}, assets)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(m.Metrics.DriftPPM) < 50 {
		t.Fatalf("drift = %.1f ppm, expected sustained drift", m.Metrics.DriftPPM)
	}
	if math.Abs(m.Metrics.DriftPPM) > 2000 {
		t.Fatalf("drift = %.1f ppm, implausible magnitude", m.Metrics.DriftPPM)
	}
}

func TestKeyframeDriftIgnoresVFRHolds(t *testing.T) {
	dir := t.TempDir()
	set, err := fixtures.Generate(dir, fixtures.SetVFR)
	if err != nil {
		t.Fatal(err)
	}
	var assets []AssetInput
	for _, a := range set.Assets {
		assets = append(assets, AssetInput{Asset: a.Name, MP4: a.MP4, Subtitle: a.Subtitle, FPS: "30000/1001"})
	}
	m, err := Run(context.Background(), Options{Mode: ModeRecover, Workers: 2, Queue: 2, FPS: "30000/1001"}, assets)
	if err != nil {
		t.Fatal(err)
	}
	// Isolated holds must not masquerade as sustained drift.
	if math.Abs(m.Metrics.DriftPPM) > 500 {
		t.Fatalf("vfr fixture drift = %.1f ppm", m.Metrics.DriftPPM)
	}
	if m.Metrics.DamageCount == 0 {
		t.Fatal("vfr fixture reported no anomalies")
	}
}

func TestTruncatedFixtureRecoveryAndStrict(t *testing.T) {
	dir := t.TempDir()
	set, err := fixtures.Generate(dir, fixtures.SetTrunc)
	if err != nil {
		t.Fatal(err)
	}
	var assets []AssetInput
	for _, a := range set.Assets {
		assets = append(assets, AssetInput{Asset: a.Name, MP4: a.MP4, Subtitle: a.Subtitle, FPS: "30000/1001"})
	}
	m, err := Run(context.Background(), Options{Mode: ModeRecover, Workers: 2, Queue: 2, FPS: "30000/1001"}, assets)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(m.Faults) == 0 || m.Metrics.DamageCount == 0 {
		t.Fatal("truncation not recorded")
	}
	if len(m.Rows) == 0 {
		t.Fatal("recovery produced no usable rows")
	}
	// No row may reference bytes beyond the file.
	rowsInDamage := 0
	for _, r := range m.Rows {
		for _, s := range m.Segments {
			if s.Source == r.Asset && s.Code == "mp4" && r.StartMs >= s.StartMs && r.StartMs <= s.EndMs {
				rowsInDamage++
			}
		}
	}
	if rowsInDamage > 0 {
		t.Fatalf("%d rows cite the truncated mdAT range", rowsInDamage)
	}
	if _, err := Run(context.Background(), Options{Mode: ModeStrict, Workers: 2, Queue: 2, FPS: "30000/1001"}, assets); err == nil {
		t.Fatal("strict mode accepted truncated media")
	}
}
