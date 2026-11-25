package pipeline

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"regress-kernel/internal/media"
)

func publishFrames() []media.Frame {
	return media.Generate(media.DefaultConfig("normal", 17)).Renditions[0].Frames
}

func TestPublishWritesSegmentsOnce(t *testing.T) {
	ws := newTestWorkspace(t)
	frames := publishFrames()
	opts := PublishOptions{TaskKey: "k", Rendition: "180p",
		Frames: frames, Clock: media.DefaultConfig("normal", 17).Clock}

	records, reused, err := ws.Publish(opts)
	if err != nil || reused {
		t.Fatalf("first publish: reused=%v err=%v", reused, err)
	}
	if len(records) != 5 {
		t.Fatalf("want 5 segments, got %d", len(records))
	}
	dir, _ := ws.segmentPath("k", records[0].Name)
	mtimeBefore := modTime(t, dir)

	// Rerun must not rewrite any artifact.
	records2, reused2, err := ws.Publish(opts)
	if err != nil || !reused2 {
		t.Fatalf("rerun must be reuse-only: reused=%v err=%v", reused2, err)
	}
	if mtimeBefore != modTime(t, dir) {
		t.Fatal("segment file was rewritten on rerun")
	}
	for i := range records {
		if records[i] != records2[i] {
			t.Fatalf("record %d drifted across reruns", i)
		}
	}
	if err := ws.VerifyArtifacts("k", records); err != nil {
		t.Fatalf("artifact verification: %v", err)
	}
}

func modTime(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime().UnixNano()
}

func TestPublishCrashBeforeCommitRecovers(t *testing.T) {
	ws := newTestWorkspace(t)
	frames := publishFrames()
	opts := PublishOptions{TaskKey: "k", Rendition: "180p", Frames: frames,
		CrashAfterWrite: true}

	// First run: bytes are durable, state commit never happens.
	records, _, err := ws.Publish(opts)
	if !errors.Is(err, ErrSimulatedCrash) {
		t.Fatalf("want simulated crash, got %v", err)
	}
	segCount := countFiles(t, ws, "k")
	if segCount != 5 {
		t.Fatalf("want 5 durable segments after crash, got %d", segCount)
	}

	// Recovery run: verify and adopt, never rewrite, exit cleanly.
	opts.CrashAfterWrite = false
	records2, reused, err := ws.Publish(opts)
	if err != nil {
		t.Fatalf("recovery must succeed: %v", err)
	}
	if !reused {
		t.Fatal("recovery must not republish any segment")
	}
	if countFiles(t, ws, "k") != segCount {
		t.Fatal("artifact count changed during recovery")
	}
	for i := range records {
		if records[i].SHA256 != records2[i].SHA256 {
			t.Fatal("recovered segment digest differs from first run")
		}
	}
}

func TestPublishDigestMismatchQuarantines(t *testing.T) {
	ws := newTestWorkspace(t)
	frames := publishFrames()
	opts := PublishOptions{TaskKey: "k", Rendition: "180p", Frames: frames}
	if _, _, err := ws.Publish(opts); err != nil {
		t.Fatal(err)
	}
	// Corrupt one on-disk segment to simulate a torn/foreign artifact.
	path, _ := ws.segmentPath("k", "seg-00001.m4s")
	if err := os.WriteFile(path, []byte("corrupted-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := ws.Publish(opts)
	if !errors.Is(err, media.ErrDigestMismatch) {
		t.Fatalf("want ErrDigestMismatch, got %v", err)
	}
	qDir, err := ws.SafeJoin("quarantine", "k")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(qDir); err != nil {
		t.Fatalf("task not quarantined: %v", err)
	}
	// The state document travels with the quarantined task directory.
	qState, err := os.ReadFile(filepath.Join(qDir, "state.json"))
	if err != nil {
		t.Fatalf("quarantined state missing: %v", err)
	}
	var st TaskState
	if err := json.Unmarshal(qState, &st); err != nil {
		t.Fatal(err)
	}
	if st.Status != StateQuarantined {
		t.Fatalf("state not quarantined: %+v", st)
	}
}

func TestPublishDeterministicAcrossWorkspaces(t *testing.T) {
	frames := publishFrames()
	opts := PublishOptions{TaskKey: "k", Rendition: "180p", Frames: frames}
	ws1 := newTestWorkspace(t)
	ws2 := newTestWorkspace(t)
	r1, _, err := ws1.Publish(opts)
	if err != nil {
		t.Fatal(err)
	}
	r2, _, err := ws2.Publish(opts)
	if err != nil {
		t.Fatal(err)
	}
	for i := range r1 {
		if r1[i].SHA256 != r2[i].SHA256 || r1[i].Size != r2[i].Size {
			t.Fatalf("segment %d not deterministic across workspaces", i)
		}
	}
}

func countFiles(t *testing.T, ws *Workspace, key string) int {
	t.Helper()
	dir, err := ws.SafeJoin("segments", sanitizeKey(key))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".m4s" {
			n++
		}
	}
	return n
}
