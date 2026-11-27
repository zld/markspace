package provenance_test

import (
	"os"
	"path/filepath"
	"testing"

	"mediasync/internal/model"
	"mediasync/internal/provenance"
)

func TestManifestRecordsAndVerifiesResult(t *testing.T) {
	dir := t.TempDir()
	result := []byte(`{"sync_error_ns":0,"duplicates":0}`)
	if err := os.WriteFile(filepath.Join(dir, "result.json"), result, 0o644); err != nil {
		t.Fatal(err)
	}
	m := provenance.New("task-1", "input-sha", "index-sha", "params-sha")
	if err := m.AttachResult(dir, "result.json"); err != nil {
		t.Fatal(err)
	}
	m.AddError(model.AtError(model.ClassRTP, "ev.jsonl", 0, 42, "bad packet"))
	manifestDigest, err := m.WriteAtomic(dir, "manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if manifestDigest == "" || m.ResultSHA256 == "" {
		t.Fatal("digests must be populated")
	}
	loaded, err := provenance.Load(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.InputSHA256 != "input-sha" || loaded.IndexSHA256 != "index-sha" {
		t.Fatalf("provenance digests lost: %+v", loaded)
	}
	if len(loaded.Errors) != 1 || loaded.Errors[0].Offset != 42 {
		t.Fatalf("structured error with offset not retained: %+v", loaded.Errors)
	}
	if err := loaded.VerifyResult(dir); err != nil {
		t.Fatalf("intact result must verify: %v", err)
	}
	// Tamper with the result: verification must fail.
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(`{"changed":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := loaded.VerifyResult(dir); model.AsMediaError(err) == nil {
		t.Fatal("tampered result must fail hash verification")
	}
}
