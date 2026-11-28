package verifier

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"mediasync/internal/fault"
	"mediasync/internal/fixture"
)

func writePair(t *testing.T, dir, name string, mp4, packets []byte) (string, string) {
	t.Helper()
	mp4Path := filepath.Join(dir, name+".mp4")
	pktPath := filepath.Join(dir, name+".bin")
	if err := os.WriteFile(mp4Path, mp4, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pktPath, packets, 0o600); err != nil {
		t.Fatal(err)
	}
	return mp4Path, pktPath
}

func scenarioByName(t *testing.T, name string) fixture.Scenario {
	t.Helper()
	for _, sc := range fixture.BuildScenarios() {
		if sc.Name == name {
			return sc
		}
	}
	t.Fatalf("scenario %s not found", name)
	return fixture.Scenario{}
}

func TestOKPairPassesWithinTwoMillis(t *testing.T) {
	sc := scenarioByName(t, fixture.NameOK)
	dir := t.TempDir()
	mp4Path, pktPath := writePair(t, dir, sc.Name, sc.MP4, sc.Packets)

	out, err := VerifyPair(context.Background(), mp4Path, pktPath)
	if err != nil {
		t.Fatalf("operational error: %v", err)
	}
	if out.Report.HasValidationFaults() {
		t.Fatalf("ok pair must have no validation faults: %+v", out.Faults)
	}
	if out.Metrics.SyncErrorMS > 2.0 {
		t.Fatalf("sync error %f ms exceeds 2 ms budget", out.Metrics.SyncErrorMS)
	}
	if out.Metrics.LossTotal != 0 || out.Metrics.RecoveryRate() != 100 {
		t.Fatalf("ok pair must be lossless with 100%% recovery: %+v", out.Metrics)
	}
}

func TestPacketLossRecoversOne(t *testing.T) {
	sc := scenarioByName(t, fixture.NamePacketLoss)
	dir := t.TempDir()
	mp4Path, pktPath := writePair(t, dir, sc.Name, sc.MP4, sc.Packets)

	out, err := VerifyPair(context.Background(), mp4Path, pktPath)
	if err != nil {
		t.Fatal(err)
	}
	if out.Metrics.LossTotal != 1 || out.Metrics.LossRecovered != 1 {
		t.Fatalf("want total=1 recovered=1, got %+v", out.Metrics)
	}
	if out.Metrics.RecoveryRate() != 100 {
		t.Fatalf("recovery rate must be 100%%, got %f", out.Metrics.RecoveryRate())
	}
}

func TestTruncatedPairReportsOffset(t *testing.T) {
	sc := scenarioByName(t, fixture.NameTruncated)
	dir := t.TempDir()
	mp4Path, pktPath := writePair(t, dir, sc.Name, sc.MP4, sc.Packets)

	out, err := VerifyPair(context.Background(), mp4Path, pktPath)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Report.HasValidationFaults() {
		t.Fatal("truncated pair must produce a validation fault")
	}
	var found bool
	for _, f := range out.Faults {
		if f.Code == fault.ErrTruncated && f.Offset > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("want attributed truncation fault with non-zero offset: %+v", out.Faults)
	}
}

func TestWrapDriftCalibrated(t *testing.T) {
	sc := scenarioByName(t, fixture.NameWrapDrift)
	dir := t.TempDir()
	mp4Path, pktPath := writePair(t, dir, sc.Name, sc.MP4, sc.Packets)

	out, err := VerifyPair(context.Background(), mp4Path, pktPath)
	if err != nil {
		t.Fatal(err)
	}
	// The wrap must be extended (no ambiguity fault) and drift calibrated.
	for _, f := range out.Faults {
		if f.Code == fault.ErrWrapAmbiguous {
			t.Fatalf("clean wrap must not be ambiguous: %v", f)
		}
		if f.Code == fault.ErrDriftTooLarge {
			t.Fatalf("fixture drift must stay under threshold: %v", f)
		}
	}
	if out.Metrics.DriftPPM == 0 {
		t.Fatal("drift fixture must report a non-zero measured drift")
	}
	if out.Metrics.SyncErrorMS > 2.0 {
		t.Fatalf("after calibration sync error %f ms must be within 2 ms", out.Metrics.SyncErrorMS)
	}
	if out.Metrics.RawSyncErrorMS <= out.Metrics.SyncErrorMS {
		t.Fatal("calibration must reduce the raw (uncorrected) sync error")
	}
}

func TestDeterministicHashAcrossRuns(t *testing.T) {
	sc := scenarioByName(t, fixture.NameOK)
	dir := t.TempDir()
	mp4Path, pktPath := writePair(t, dir, sc.Name, sc.MP4, sc.Packets)

	first, err := VerifyPair(context.Background(), mp4Path, pktPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := VerifyPair(context.Background(), mp4Path, pktPath)
	if err != nil {
		t.Fatal(err)
	}
	if first.Report.ContentHash != second.Report.ContentHash {
		t.Fatal("repeated runs must produce identical content_hash")
	}
	if len(first.Report.Faults) != len(second.Report.Faults) {
		t.Fatal("fault list length must be stable across runs")
	}
}
