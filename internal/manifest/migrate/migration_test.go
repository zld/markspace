package migrate_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mediaprep/internal/manifest"
	"mediaprep/internal/manifest/migrate"
	"mediaprep/internal/model"
)

func TestV1PrepareInterruptResumeAndRollback(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	doc := &manifest.Document{
		Root: root, SchemaVersion: manifest.Version1, CreatedAt: now.Format(time.RFC3339Nano),
		Summary: manifest.Summary{Segments: 3, Silent: 1, Duplicates: 1, MetadataConflicts: 3, Verdict: "PASS"},
	}
	initial, err := migrate.WriteInitialV1(root, doc, now)
	if err != nil {
		t.Fatal(err)
	}
	pointerPath := filepath.Join(root, migrate.ManifestDir, migrate.PointerFile)
	before, err := os.ReadFile(pointerPath)
	if err != nil {
		t.Fatal(err)
	}

	plan, err := migrate.Prepare(root, "v1", true, now.Add(time.Minute))
	if plan == nil {
		t.Fatal("prepare returned no plan")
	}
	if code := model.InputExitCode(err); code != 3 {
		t.Fatalf("prepare stop exit code=%d err=%v", code, err)
	}
	if !errors.Is(err, model.ErrMigrationStopped) {
		t.Fatalf("error does not wrap migration sentinel: %v", err)
	}
	afterPrepare, err := os.ReadFile(pointerPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(afterPrepare) {
		t.Fatal("prepare changed v1 current pointer")
	}
	if got := sha256ForTest(afterPrepare); got != initial.PointerSHA256 {
		t.Fatalf("pointer hash changed: %s -> %s", initial.PointerSHA256, got)
	}
	if _, err := os.Stat(filepath.Join(root, migrate.ManifestDir, migrate.V2File)); err != nil {
		t.Fatalf("prepared v2 missing: %v", err)
	}

	res, err := migrate.Resume(root, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if res.Pointer.Version != manifest.Version2 {
		t.Fatalf("current=%s want v2", res.Pointer.Version)
	}
	current, err := os.ReadFile(pointerPath)
	if err != nil {
		t.Fatal(err)
	}
	if res.PointerSHA256 != sha256ForTest(current) {
		t.Fatal("returned pointer hash does not match atomically installed file")
	}
	rb, err := os.ReadFile(filepath.Join(root, migrate.ManifestDir, migrate.RollbackV1))
	if err != nil {
		t.Fatalf("v1 rollback point missing: %v", err)
	}
	if string(rb) != string(before) {
		t.Fatal("rollback pointer is not exact original v1 pointer")
	}

	back, err := migrate.Rollback(root, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if back.Pointer.Version != manifest.Version1 || back.PointerSHA256 != sha256ForTest(before) {
		t.Fatalf("rollback did not restore v1: %+v", back.Pointer)
	}
}

func sha256ForTest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
