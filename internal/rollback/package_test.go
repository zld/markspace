package rollback_test

import (
	"os"
	"path/filepath"
	"testing"

	"mediarepair/internal/batch"
	"mediarepair/internal/media"
	"mediarepair/internal/rollback"
	"mediarepair/internal/testfixture"
)

func completedRun(t *testing.T) (string, *batch.Result) {
	t.Helper()
	root := t.TempDir()
	if _, err := testfixture.CreateStandard(root); err != nil {
		t.Fatal(err)
	}
	res, err := batch.Run(batch.Options{Root: root, BlockSize: 4})
	if err != nil {
		t.Fatal(err)
	}
	return root, res
}

func publish(t *testing.T, root string, res *batch.Result, platform, version string) *rollback.PackageManifest {
	t.Helper()
	pm, err := rollback.Publish(rollback.PublishOptions{
		Root:             root,
		ArtifactsRoot:    root,
		Platform:         platform,
		Version:          version,
		InputFingerprint: res.InputFingerprint,
		Index:            res.Index,
		IndexSHA256:      res.IndexSHA256,
	})
	if err != nil {
		t.Fatalf("publish %s: %v", version, err)
	}
	return pm
}

func TestPublishVerifyAndRollbackPrevious(t *testing.T) {
	root, res := completedRun(t)
	platform := "darwin-arm64"
	v1 := publish(t, root, res, platform, "v1")
	v2 := publish(t, root, res, platform, "v2")
	if v1.PackageSHA256 == v2.PackageSHA256 {
		t.Fatal("version/package hashes should differ by version manifest")
	}
	cur, err := rollback.ReadCurrent(root, platform)
	if err != nil || cur.Version != "v2" || cur.PreviousVersion != "v1" {
		t.Fatalf("current=%+v err=%v", cur, err)
	}

	rolled, err := rollback.Rollback(root, platform)
	if err != nil {
		t.Fatal(err)
	}
	if rolled.Version != "v1" || rolled.PackageSHA256 != v1.PackageSHA256 {
		t.Fatalf("rolled=%+v", rolled)
	}
	cur, _ = rollback.ReadCurrent(root, platform)
	if cur.Version != "v1" || cur.PackageSHA256 != v1.PackageSHA256 {
		t.Fatalf("pointer after rollback=%+v", cur)
	}
	verified, err := rollback.VerifyPackage(filepath.Join(root, "packages", platform, "versions", "v1"))
	if err != nil || verified.PackageSHA256 != v1.PackageSHA256 {
		t.Fatalf("reverify err=%v manifest=%+v", err, verified)
	}
}

func TestFailedPublishLeavesCurrentPointer(t *testing.T) {
	root, res := completedRun(t)
	platform := "ios-arm64"
	publish(t, root, res, platform, "v1")

	// Remove an artifact required by the next package.
	if err := os.Remove(filepath.Join(root, "segments", "segment-002.bin")); err != nil {
		t.Fatal(err)
	}
	_, err := rollback.Publish(rollback.PublishOptions{
		Root: root, ArtifactsRoot: root, Platform: platform, Version: "v-bad",
		InputFingerprint: res.InputFingerprint, Index: res.Index, IndexSHA256: res.IndexSHA256,
	})
	if err == nil {
		t.Fatal("expected publish failure for missing segment")
	}
	cur, err := rollback.ReadCurrent(root, platform)
	if err != nil || cur.Version != "v1" {
		t.Fatalf("current should remain v1, got %+v err=%v code=%v", cur, err, media.AsError(err))
	}
	if _, err := os.Stat(filepath.Join(root, "packages", platform, "versions", "v-bad")); !os.IsNotExist(err) {
		t.Fatalf("failed version directory should not be published, stat err=%v", err)
	}
}

func TestCorruptInputDoesNotAdvanceCurrent(t *testing.T) {
	root, res := completedRun(t)
	platform := "android-arm64"
	publish(t, root, res, platform, "v1")

	framePath := filepath.Join(root, res.Index.Records[0].RelativePath)
	f, err := os.OpenFile(framePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := batch.Run(batch.Options{Root: root, BlockSize: 4}); !media.HasCode(err, media.CodeChecksum) {
		t.Fatalf("corrupt frame batch err=%v", err)
	}
	cur, err := rollback.ReadCurrent(root, platform)
	if err != nil || cur.Version != "v1" {
		t.Fatalf("current pointer changed after corrupt input: %+v err=%v", cur, err)
	}
}
