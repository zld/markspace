package migrate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAdaptV1AndIdempotentV2(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "catalog.json")
	v1 := `{"version":1,"playlist":"video.m3u8","media_sequence":7,
"items":[{"uri":"a.m4s","duration_ms":1000},{"uri":"b.m4s","duration_ns":500000000}]}`
	if err := os.WriteFile(p, []byte(v1), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := MigrateFile(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.AlreadyV2 || rep.Checkpoints != 1 {
		t.Fatalf("report=%+v", rep)
	}
	v2, _, err := LoadVersioned(p)
	if err != nil || v2.Schema != SchemaV2 || len(v2.Segments) != 2 {
		t.Fatalf("v2=%+v err=%v", v2, err)
	}
	if v2.Segments[0].DurationNS != 1_000_000_000 || v2.Segments[1].Sequence != 8 {
		t.Fatalf("adaptation wrong: %+v", v2.Segments)
	}
	rep2, err := MigrateFile(p, Options{})
	if err != nil || !rep2.AlreadyV2 || rep2.Checkpoints != 1 {
		t.Fatalf("rerun rep=%+v err=%v", rep2, err)
	}
}

func TestFailedSwitchVerificationRestoresCheckpoint(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "catalog.json")
	old := `{"version":1,"items":[{"uri":"a.m4s","duration_ns":1}]}`
	if err := os.WriteFile(p, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("post switch checksum failed")
	rep, err := MigrateFile(p, Options{PostSwitchVerify: func(*ManifestV2) error { return boom }})
	if err == nil {
		t.Fatal("expected migration failure")
	}
	if !rep.RolledBack {
		t.Fatalf("expected rollback: %+v", rep)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != old {
		t.Fatalf("old manifest not restored:\n%s", got)
	}
	if _, err := os.Stat(CheckpointPath(p)); err != nil {
		t.Fatalf("checkpoint not preserved: %v", err)
	}
}
