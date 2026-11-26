package journal

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"continuity/internal/media"
)

func sampleArtifacts() ([]Artifact, []FrameRecord) {
	frames := []FrameRecord{
		{Sequence: 1, File: "frames/0001.png", SHA256: sha("one"), Sources: []string{"v.m4s"}},
		{Sequence: 2, File: "frames/0002.png", SHA256: sha("two"), Sources: []string{"a.m4s"}},
	}
	arts := []Artifact{
		{Name: frames[0].File, Data: []byte("one"), SHA: frames[0].SHA256},
		{Name: frames[1].File, Data: []byte("two"), SHA: frames[1].SHA256},
	}
	sum := ChecksumSummary(arts)
	arts = append(arts, sum)
	return arts, frames
}

func TestCrashThenRecoverCommitsOnce(t *testing.T) {
	dir := t.TempDir()
	arts, frames := sampleArtifacts()
	_, err := Commit(dir, "recovery", "tx-1", arts, frames, []string{"v.m4s"}, true)
	if err == nil {
		t.Fatal("expected injected crash")
	}
	if code := media.ExitCode(err); code != 75 {
		t.Fatalf("exit code=%d err=%v", code, err)
	}
	if _, err := os.Stat(filepath.Join(dir, stateFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state must not exist, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, checksumsFile)); err != nil {
		t.Fatalf("checksums must be durable: %v", err)
	}

	rep2, err := Commit(dir, "recovery", "tx-1", arts, frames, []string{"v.m4s"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.RecoveredArtifacts != 1 || rep2.Published != 2 || rep2.Duplicates != 0 {
		t.Fatalf("recovery report=%+v", rep2)
	}
	rep3, err := Commit(dir, "recovery", "tx-1", arts, frames, []string{"v.m4s"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !rep3.AlreadyCommitted || rep3.Published != 2 {
		t.Fatalf("idempotent report=%+v", rep3)
	}
}

func TestHashMismatchStopsAndPreservesFile(t *testing.T) {
	dir := t.TempDir()
	arts, frames := sampleArtifacts()
	if _, err := Commit(dir, "clean", "tx", arts, frames, nil, false); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "frames/0001.png")
	if err := os.WriteFile(p, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Commit(dir, "clean", "tx", arts, frames, nil, false)
	f, ok := media.AsFault(err)
	if !ok || f.Code != media.CodeHashMismatch {
		t.Fatalf("want hash mismatch, got %v", err)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "tampered" {
		t.Fatal("scene was not preserved")
	}
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
