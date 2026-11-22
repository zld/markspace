package pipeline

import (
	"context"
	"math"
	"os"
	"path/filepath"
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

func TestCueTimecodeShiftIsLocated(t *testing.T) {
	dir := t.TempDir()
	srt := "1\n00:00:02,000 --> 00:00:03,000\nslate TC 00:00:10;00 is wrong\n\n"
	path := filepath.Join(dir, "shift.srt")
	if err := os.WriteFile(path, []byte(srt), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Run(context.Background(), Options{Mode: ModeRecover, Workers: 1, Queue: 1, FPS: "30000/1001"}, []AssetInput{
		{Asset: "shift", Subtitle: path, FPS: "30000/1001", LabelFormat: "29.97df"},
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range m.Faults {
		if f.Code == "timestamp_rollback" && f.HasMedia {
			found = true
		}
	}
	if !found {
		t.Fatalf("shift not reported: %v", m.Faults)
	}
}

func TestMixedSeparatorsAreAmbiguous(t *testing.T) {
	dir := t.TempDir()
	srt := "1\n00:00:01,000 --> 00:00:02,000\nTC 00:00:01;00 then TC 00:00:01:05\n\n"
	path := filepath.Join(dir, "mix.srt")
	if err := os.WriteFile(path, []byte(srt), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Run(context.Background(), Options{Mode: ModeRecover, Workers: 1, Queue: 1, FPS: "25"}, []AssetInput{
		{Asset: "mix", Subtitle: path, FPS: "25"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Faults) == 0 {
		t.Fatal("mixed separators accepted silently")
	}
}
