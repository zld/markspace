package publish_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"media-check/internal/assets"
	"media-check/internal/fault"
	"media-check/internal/publish"
)

func src() *assets.Source { return assets.Seal("capture", []byte("seed-17-capture")) }

func artifactBytes() []byte { return []byte("FAKE-MP4-CONTENT-v1") }

// expectedKey builds the recovery expectation map for one artifact.
func expectedKey(version, name, digest string) map[string]string {
	return map[string]string{version + "/" + name: digest}
}

func TestNormalPublishCommitsOnce(t *testing.T) {
	dir := t.TempDir()
	ledger, err := publish.OpenLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := src()
	data := artifactBytes()
	a, err := ledger.Publish("v1", "bundle.mp4", s, data)
	if err != nil {
		t.Fatal(err)
	}
	if a.State != publish.StateCommitted {
		t.Fatalf("state = %s, want committed", a.State)
	}
	if got, ok := ledger.Find("v1", "bundle.mp4"); !ok || got.Digest != a.Digest {
		t.Fatal("committed artifact not findable")
	}
	// ledger.json must exist and be non-empty.
	if info, err := os.Stat(filepath.Join(dir, "ledger.json")); err != nil || info.Size() == 0 {
		t.Fatalf("ledger file missing/empty: %v", err)
	}
}

func TestCrashThenRerunReusesArtifactWithoutRepublish(t *testing.T) {
	dir := t.TempDir()
	s := src()
	data := artifactBytes()
	digest := assets.Digest(data)

	// First process: bytes land on disk, ledger never commits.
	func() {
		ledger, err := publish.OpenLedger(dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.CrashAfterArtifact("v1", "bundle.mp4", s, data); err != nil {
			t.Fatal(err)
		}
		// Process "exits" here without Commit: ledger.json is absent.
	}()
	if _, err := os.Stat(filepath.Join(dir, "ledger.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("crash must leave no ledger file, got err=%v", err)
	}
	// One artifact file is nevertheless on disk.
	artDir := filepath.Join(dir, "artifacts")
	if entries, _ := os.ReadDir(artDir); len(entries) != 1 {
		t.Fatalf("expected 1 orphan artifact on disk, got %v", entries)
	}

	// Rerun: a brand-new ledger process reconciles the orphan.
	ledger2, err := publish.OpenLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := ledger2.Recover(expectedKey("v1", "bundle.mp4", digest))
	if err != nil {
		t.Fatal(err)
	}
	// The required assertions: reuse exactly 1, no republish, no missed.
	if rep.Reused != 1 {
		t.Fatalf("reused = %d, want 1", rep.Reused)
	}
	if rep.Republished != 0 {
		t.Fatalf("republished = %d, want 0", rep.Republished)
	}
	if rep.MissedLedger != 1 {
		t.Fatalf("missed ledger entries = %d, want 1 detected", rep.MissedLedger)
	}
	if rep.Quarantined != 0 {
		t.Fatalf("quarantined = %d, want 0", rep.Quarantined)
	}
	a, ok := ledger2.Find("v1", "bundle.mp4")
	if !ok || a.State != publish.StateCommitted {
		t.Fatalf("orphan not re-adopted as committed: %+v ok=%v", a, ok)
	}
}

func TestDigestMismatchQuarantinesAndRegenerates(t *testing.T) {
	dir := t.TempDir()
	s := src()
	data := artifactBytes()

	ledger, err := publish.OpenLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.CrashAfterArtifact("v1", "bundle.mp4", s, data); err != nil {
		t.Fatal(err)
	}
	// Tamper the orphan before rerun so its digest no longer matches.
	artDir := filepath.Join(dir, "artifacts")
	entries, _ := os.ReadDir(artDir)
	if len(entries) != 1 {
		t.Fatalf("want 1 artifact, got %d", len(entries))
	}
	if err := os.WriteFile(filepath.Join(artDir, entries[0].Name()), []byte("CORRUPTED"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Rerun expects the ORIGINAL digest: mismatch must quarantine.
	ledger2, err := publish.OpenLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := ledger2.Recover(expectedKey("v1", "bundle.mp4", assets.Digest(data)))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Quarantined != 1 || rep.Reused != 0 {
		t.Fatalf("expected quarantine, got %+v", rep)
	}
	qEntries, _ := os.ReadDir(filepath.Join(dir, "quarantine"))
	if len(qEntries) != 1 {
		t.Fatalf("quarantine should hold 1 file, got %d", len(qEntries))
	}
	// Regeneration path: publish fresh bytes, now commits cleanly.
	a, err := ledger2.Publish("v1", "bundle.mp4", s, data)
	if err != nil {
		t.Fatal(err)
	}
	if a.State != publish.StateCommitted || a.Digest != assets.Digest(data) {
		t.Fatalf("regenerated artifact wrong: %+v", a)
	}
}

func TestRecoveryIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s := src()
	data := artifactBytes()
	ledger, err := publish.OpenLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Publish("v1", "bundle.mp4", s, data); err != nil {
		t.Fatal(err)
	}
	// A second recovery over already-committed state changes nothing.
	rep, err := ledger.Recover(expectedKey("v1", "bundle.mp4", assets.Digest(data)))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Reused != 0 || rep.MissedLedger != 0 || rep.Quarantined != 0 {
		t.Fatalf("idempotent recovery must be a no-op, got %+v", rep)
	}
}

func TestInvariantViolationDetected(t *testing.T) {
	dir := t.TempDir()
	s := src()
	data := artifactBytes()
	ledger, err := publish.OpenLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := ledger.Publish("v1", "bundle.mp4", s, data)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.VerifyInvariants(); err != nil {
		t.Fatalf("healthy ledger must pass invariants: %v", err)
	}
	// Corrupt a committed artifact behind the ledger's back.
	if err := os.WriteFile(filepath.Join(dir, a.Path), []byte("DRIFT"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ledger.VerifyInvariants(); !errors.Is(err, fault.ErrRecover) {
		t.Fatalf("digest drift must map to ErrRecover, got %v", err)
	}
}
