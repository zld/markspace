package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeJoinRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"../evil.mp4", "segments/../../evil.mp4", "/absolute/evil.mp4"} {
		if _, err := SafeJoin(root, bad); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
	got, err := SafeJoin(root, "segments/segment-001.mp4")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, got)
	if err != nil || rel != filepath.Join("segments", "segment-001.mp4") {
		t.Fatalf("safe path mismatch: %s rel=%s err=%v", got, rel, err)
	}
}

func TestManifestValidationDetectsTamperAndMissingGapReference(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(RunOptions{Fixture: "clean", Out: dir})
	if err != nil {
		t.Fatal(err)
	}
	if rep := ValidateManifest(dir, res.Manifest); !rep.Valid {
		t.Fatalf("fresh manifest invalid: %#v", rep)
	}

	target, err := SafeJoin(dir, res.Manifest.Segments[0].File)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(target, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("tamper"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	rep := ValidateManifest(dir, res.Manifest)
	if rep.Valid || rep.ErrorCount == 0 {
		t.Fatal("tampered segment did not invalidate manifest")
	}

	missing := *res.Manifest
	missing.Segments = append([]ManifestSegment(nil), res.Manifest.Segments...)
	missing.Segments[0].GapIDs = []string{"does-not-exist"}
	rep = ValidateManifest(dir, &missing)
	found := false
	for _, msg := range rep.Errors {
		if strings.Contains(msg, "missing gap") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing gap reference not reported: %#v", rep.Errors)
	}
}
