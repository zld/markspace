package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"regress-kernel/internal/media"
)

func newTestWorkspace(t *testing.T) *Workspace {
	t.Helper()
	ws, err := OpenWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	return ws
}

func TestSafeJoinRejectsEscape(t *testing.T) {
	ws := newTestWorkspace(t)
	good, err := ws.SafeJoin("segments", "seg-1.m4s")
	if err != nil {
		t.Fatalf("safe join failed: %v", err)
	}
	if !filepath.IsAbs(good) {
		t.Fatal("workspace path must be absolute")
	}
	for _, bad := range [][]string{
		{"segments", "../../../etc/passwd"},
		{"/etc/passwd"},
		{"segments", "../", "..", "evil"},
	} {
		if _, err := ws.SafeJoin(bad...); !errors.Is(err, media.ErrPathEscape) {
			t.Fatalf("path %v not rejected: %v", bad, err)
		}
	}
}

func TestSanitizeKey(t *testing.T) {
	cases := map[string]string{
		"regress-normal-180p": "regress-normal-180p",
		"../evil":             ".._evil",
		"a/b\\c":              "a_b_c",
	}
	for in, want := range cases {
		if got := sanitizeKey(in); got != want {
			t.Fatalf("sanitizeKey(%q)=%q want %q", in, got, want)
		}
	}
}

func TestStateRoundTripAndMissing(t *testing.T) {
	ws := newTestWorkspace(t)
	if _, ok, err := ws.LoadState("k"); err != nil || ok {
		t.Fatalf("fresh task should miss cleanly: ok=%v err=%v", ok, err)
	}
	st := &TaskState{
		TaskKey: "k", Rendition: "180p", Status: StateRunning, Stage: StageAlign,
		UpdatedAt:    "run-1/align",
		InputSummary: InputSummary{FrameCount: 50, FrameHash: "abc"},
	}
	if err := ws.SaveState(st); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok, err := ws.LoadState("k")
	if err != nil || !ok {
		t.Fatalf("reload: ok=%v err=%v", ok, err)
	}
	if got.Stage != StageAlign || got.InputSummary.FrameCount != 50 {
		t.Fatalf("state content lost: %+v", got)
	}
	// No temp files must survive the atomic rename.
	entries, _ := os.ReadDir(filepath.Dir(mustStatePath(t, ws, "k")))
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatal("temporary state file left behind")
		}
	}
}

func mustStatePath(t *testing.T, ws *Workspace, key string) string {
	t.Helper()
	p, err := ws.statePath(key)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCheckpointChainResume(t *testing.T) {
	ws := newTestWorkspace(t)
	digest := "digest-v1"
	for _, stage := range Stages[:2] {
		if _, err := ws.WriteCheckpoint("k", stage, digest,
			map[string]interface{}{"stage": string(stage)}); err != nil {
			t.Fatalf("write checkpoint: %v", err)
		}
	}
	cp, err := ws.LastCheckpoint("k", digest)
	if err != nil {
		t.Fatalf("last checkpoint: %v", err)
	}
	if cp == nil || cp.Stage != StageAlign {
		t.Fatalf("want align checkpoint, got %+v", cp)
	}
	// Changed input digest invalidates the chain: rerun starts from ingest.
	cp2, err := ws.LastCheckpoint("k", "digest-v2")
	if err != nil {
		t.Fatalf("digest mismatch should not error: %v", err)
	}
	if cp2 != nil {
		t.Fatalf("changed inputs must invalidate checkpoints, got %+v", cp2)
	}
}

func TestCheckpointIdempotentRewrite(t *testing.T) {
	ws := newTestWorkspace(t)
	first, err := ws.WriteCheckpoint("k", StageIngest, "d", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ws.WriteCheckpoint("k", StageIngest, "d", nil)
	if err != nil || first.InputDigest != second.InputDigest {
		t.Fatalf("checkpoint rewrite unstable: %v", err)
	}
	exists, _ := ws.CheckpointExists("k", StageIngest)
	if !exists {
		t.Fatal("checkpoint should exist")
	}
}

func TestQuarantineMovesTask(t *testing.T) {
	ws := newTestWorkspace(t)
	st := &TaskState{TaskKey: "bad-task", Status: StateFailed, Stage: StagePublish}
	if err := ws.SaveState(st); err != nil {
		t.Fatal(err)
	}
	dst, err := ws.Quarantine(st, "digest mismatch")
	if err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("quarantine dir missing: %v", err)
	}
	src, _ := ws.taskDir("bad-task")
	if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("original task dir should be gone")
	}
	if st.Status != StateQuarantined {
		t.Fatalf("state not marked quarantined: %s", st.Status)
	}
}

func TestInputDigestStable(t *testing.T) {
	a, _ := InputDigest(InputSummary{FrameCount: 50, FrameHash: "x"})
	b, _ := InputDigest(InputSummary{FrameCount: 50, FrameHash: "x"})
	c, _ := InputDigest(InputSummary{FrameCount: 51, FrameHash: "x"})
	if a != b || a == c {
		t.Fatal("input digest must be stable and content-sensitive")
	}
}
