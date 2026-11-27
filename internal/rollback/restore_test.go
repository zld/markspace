package rollback_test

import (
	"errors"
	"testing"

	"media-check/internal/assets"
	"media-check/internal/publish"
	"media-check/internal/rollback"
)

func setupVersions(t *testing.T, dir string) (*publish.Ledger, *rollback.Manager, *assets.Source) {
	t.Helper()
	ledger, err := publish.OpenLedger(dir + "/pub")
	if err != nil {
		t.Fatal(err)
	}
	rm, err := rollback.NewManager(dir + "/rb")
	if err != nil {
		t.Fatal(err)
	}
	s := assets.Seal("capture", []byte("seed-17"))
	// v1: one artifact.
	if _, err := ledger.Publish("v1", "bundle-v1.mp4", s, []byte("v1-bytes")); err != nil {
		t.Fatal(err)
	}
	mf1, err := rm.Snapshot("v1", s.ID, ledger)
	if err != nil {
		t.Fatal(err)
	}
	// v2: a new artifact under a new source.
	s2 := assets.Seal("capture", []byte("seed-18"))
	if _, err := ledger.Publish("v2", "bundle-v2.mp4", s2, []byte("v2-bytes")); err != nil {
		t.Fatal(err)
	}
	mf2, err := rm.Snapshot("v2", s2.ID, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if rollback.Digest(mf1) == rollback.Digest(mf2) {
		t.Fatal("different versions must have different manifest digests")
	}
	return ledger, rm, s
}

func TestRollbackRestoresTargetManifest(t *testing.T) {
	dir := t.TempDir()
	ledger, rm, s := setupVersions(t, dir)
	_ = s

	mf2, err := rm.Rollback("v2", ledger)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := rm.Current()
	if err != nil {
		t.Fatal(err)
	}
	if cur.Version != "v2" || rollback.Digest(cur) != rollback.Digest(mf2) {
		t.Fatalf("current = %s, want v2", cur.Version)
	}

	// Roll back to v1: current pointer must match the v1 manifest digest.
	mf1, err := rm.Rollback("v1", ledger)
	if err != nil {
		t.Fatal(err)
	}
	cur, err = rm.Current()
	if err != nil {
		t.Fatal(err)
	}
	if cur.Version != "v1" {
		t.Fatalf("after rollback current = %s, want v1", cur.Version)
	}
	if rollback.Digest(cur) != rollback.Digest(mf1) {
		t.Fatal("rolled-back manifest digest does not match stored v1 manifest")
	}
	if len(cur.Items) != 1 || cur.Items[0].Digest != assets.Digest([]byte("v1-bytes")) {
		t.Fatalf("v1 manifest items wrong: %+v", cur.Items)
	}
}

func TestAuditTrailRecordsTransitions(t *testing.T) {
	dir := t.TempDir()
	ledger, rm, _ := setupVersions(t, dir)
	if _, err := rm.Rollback("v2", ledger); err != nil {
		t.Fatal(err)
	}
	if _, err := rm.Rollback("v1", ledger); err != nil {
		t.Fatal(err)
	}
	audit, err := rm.Audit()
	if err != nil {
		t.Fatal(err)
	}
	// 2 snapshots + 2 rollbacks.
	if len(audit) != 4 {
		t.Fatalf("audit entries = %d, want 4: %+v", len(audit), audit)
	}
	last := audit[len(audit)-1]
	if last.Action != "rollback" || last.FromVersion != "v2" || last.ToVersion != "v1" {
		t.Fatalf("last transition wrong: %+v", last)
	}
	if last.ManifestDigest == "" || last.SourceDigest == "" {
		t.Fatal("audit must retain manifest and source digests")
	}
}

func TestUnknownVersionFails(t *testing.T) {
	dir := t.TempDir()
	ledger, rm, _ := setupVersions(t, dir)
	if _, err := rm.Rollback("v99", ledger); !errors.Is(err, rollback.ErrUnknownVersion) {
		t.Fatalf("unknown version must fail with ErrUnknownVersion, got %v", err)
	}
}
