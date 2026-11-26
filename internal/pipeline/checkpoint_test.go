package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"rtx/internal/model"
)

func TestCheckpointRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.json")
	cp := &Checkpoint{ManifestSum: "m", InputSum: "i", ParamsSum: "p", Cursor: 2, Total: 4}
	if err := WriteCheckpoint(path, cp); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCheckpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cursor != 2 || got.Total != 4 || got.Version != CheckpointVersion {
		t.Fatalf("checkpoint = %+v", got)
	}
}

func TestCheckpointMissingIsNil(t *testing.T) {
	cp, err := LoadCheckpoint(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || cp != nil {
		t.Fatalf("cp=%v err=%v", cp, err)
	}
}

func TestCheckpointCorruptRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckpoint(path); err == nil {
		t.Fatal("expected corrupt checkpoint error")
	}
}

func TestCheckpointWrongVersionRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"cursor":0,"total":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadCheckpoint(path)
	if err == nil {
		t.Fatal("expected version error")
	}
	if e, _ := model.As(err); e == nil || e.Code != CodeCheckpoint {
		t.Fatalf("error = %v", err)
	}
}

func TestReconcileRejectsChangedManifest(t *testing.T) {
	cp := &Checkpoint{ManifestSum: "a", InputSum: "b", ParamsSum: "c", Total: 4}
	_, err := Reconcile(cp, "different", "b", "c", 4)
	if err == nil {
		t.Fatal("expected manifest mismatch")
	}
}

func TestReconcileRejectsChangedParams(t *testing.T) {
	cp := &Checkpoint{ManifestSum: "a", InputSum: "b", ParamsSum: "c", Total: 4}
	_, err := Reconcile(cp, "a", "b", "other", 4)
	if err == nil {
		t.Fatal("expected params mismatch")
	}
}

func TestReconcileCompletedReportsResume(t *testing.T) {
	cp := &Checkpoint{ManifestSum: "a", InputSum: "b", ParamsSum: "c", Total: 4, Cursor: 4, Completed: true}
	d, err := Reconcile(cp, "a", "b", "c", 4)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Resuming || d.Reason != "already_completed" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestVerifyArtifactsDetectsChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, size, err := FileDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := ArtifactRecord{Name: "a.bin", SHA256: sum, Size: size}
	if err := VerifyArtifacts(dir, []ArtifactRecord{rec}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifacts(dir, []ArtifactRecord{rec}); err == nil {
		t.Fatal("expected tamper detection")
	}
}
