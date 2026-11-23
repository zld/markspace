package journal

import (
	"os"
	"path/filepath"
	"testing"

	"mediaroute/internal/clock"
)

func newTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir
}

func TestHappyPathCommitAndReplay(t *testing.T) {
	dir := newTestDir(t)
	c, err := OpenCoordinator(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	art := map[string][]byte{"frame-001.bin": {1, 2, 3, 4}}
	if err := c.CommitStage("seg-001", art); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	// Published artifact must exist on disk.
	got, err := os.ReadFile(filepath.Join(dir, segDir, "seg-001", "frame-001.bin"))
	if err != nil || string(got) != string([]byte{1, 2, 3, 4}) {
		t.Fatalf("published artifact wrong: %v %v", got, err)
	}
	// Restart: recovery replays the committed stage.
	rep, err := Recover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Replayed) != 1 || rep.Replayed[0] != "seg-001" || rep.Isolated {
		t.Fatalf("replay report wrong: %+v", rep)
	}
	if v := rep.Artifacts["seg-001/frame-001.bin"]; len(v) != 4 {
		t.Fatalf("replayed artifact missing: %+v", rep.Artifacts)
	}
}

func TestCrashBeforeCommitRollsBack(t *testing.T) {
	dir := newTestDir(t)
	hook := func(point string) error {
		if point == PointAfterWALData+":seg-002" {
			return os.ErrPermission // simulate crash before COMMIT
		}
		return nil
	}
	c, err := OpenCoordinator(dir, hook)
	if err != nil {
		t.Fatal(err)
	}
	err = c.CommitStage("seg-002", map[string][]byte{"k.bin": {9}})
	if err == nil {
		t.Fatal("simulated crash was not surfaced")
	}
	if pe, ok := clock.AsError(err); !ok || !pe.Recoverable || pe.Phase != clock.PhaseJournal {
		t.Fatalf("crash error model wrong: %v", err)
	}
	c.Close()

	// Nothing committed may be visible after restart.
	rep, err := Recover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Replayed) != 0 {
		t.Fatalf("uncommitted stage replayed: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(dir, segDir, "seg-002")); !os.IsNotExist(err) {
		t.Fatalf("uncommitted segment directory leaked: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, walTmpName)); !os.IsNotExist(err) {
		t.Fatal("staging temp directory survived rollback")
	}
}

func TestCrashAfterCommitRecovers(t *testing.T) {
	dir := newTestDir(t)
	hook := func(point string) error {
		// COMMIT record is already durable when this point fires.
		if point == PointAfterWALCommit+":seg-003" {
			return os.ErrPermission
		}
		return nil
	}
	c, _ := OpenCoordinator(dir, hook)
	err := c.CommitStage("seg-003", map[string][]byte{"k.bin": {7, 7}})
	if err == nil {
		t.Fatal("expected simulated crash error")
	}
	c.Close()

	rep, err := Recover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Replayed) != 1 || rep.Replayed[0] != "seg-003" {
		t.Fatalf("committed stage lost after crash: %+v", rep)
	}
	got, err := os.ReadFile(filepath.Join(dir, segDir, "seg-003", "k.bin"))
	if err != nil || len(got) != 2 {
		t.Fatalf("replay did not republish artifact: %v %v", got, err)
	}
}

func TestCorruptTailIsolated(t *testing.T) {
	dir := newTestDir(t)
	c, _ := OpenCoordinator(dir, nil)
	if err := c.CommitStage("seg-004", map[string][]byte{"a.bin": {1}}); err != nil {
		t.Fatal(err)
	}
	c.Close()

	walPath := filepath.Join(dir, walName)
	info, _ := os.Stat(walPath)
	goodLen := info.Size()
	// Append garbage that looks like the start of a record but fails framing.
	f, _ := os.OpenFile(walPath, os.O_APPEND|os.O_WRONLY, 0o644)
	f.Write([]byte{0x09, 0x00, 0x10, 0x00, 0x00, 0x00, 0x40, 0xAA})
	f.Close()

	rep, err := Recover(dir)
	if err != nil {
		t.Fatalf("recovery should isolate, not fail: %v", err)
	}
	if !rep.Isolated || rep.TailOffset != goodLen {
		t.Fatalf("tail isolation report wrong: %+v goodLen=%d", rep, goodLen)
	}
	// The committed stage before corruption must still replay.
	if len(rep.Replayed) != 1 || rep.Replayed[0] != "seg-004" {
		t.Fatalf("valid prefix lost: %+v", rep)
	}
	// WAL now ends exactly at the valid prefix.
	info2, _ := os.Stat(walPath)
	if info2.Size() != goodLen {
		t.Fatalf("WAL not truncated: %d != %d", info2.Size(), goodLen)
	}
	// A second recovery is clean and repeatable.
	rep2, err := Recover(dir)
	if err != nil || rep2.Isolated {
		t.Fatalf("repeat recovery not stable: %+v %v", rep2, err)
	}
}

func TestCRCMismatchIsolated(t *testing.T) {
	dir := newTestDir(t)
	c, _ := OpenCoordinator(dir, nil)
	c.CommitStage("seg-005", map[string][]byte{"b.bin": {2}})
	c.Close()

	walPath := filepath.Join(dir, walName)
	data, _ := os.ReadFile(walPath)
	// Flip a payload byte in the last record so its CRC check fails.
	corrupt := append([]byte(nil), data...)
	corrupt[len(corrupt)-1] ^= 0xFF
	os.WriteFile(walPath, corrupt, 0o644)

	rep, err := Recover(dir)
	if err != nil {
		t.Fatalf("CRC failure must be isolated, not fatal: %v", err)
	}
	if !rep.Isolated {
		t.Fatal("CRC mismatch did not trigger isolation")
	}
}

func TestAbortDiscardsStage(t *testing.T) {
	dir := newTestDir(t)
	c, _ := OpenCoordinator(dir, nil)
	_ = c.wal.Append(Record{Kind: RecBegin, Key: "seg-006"})
	_ = c.wal.Append(Record{Kind: RecData, Key: "seg-006/x.bin", Value: []byte{1}})
	if err := c.Abort("seg-006"); err != nil {
		t.Fatal(err)
	}
	c.Close()
	rep, err := Recover(dir)
	if err != nil || len(rep.Replayed) != 0 {
		t.Fatalf("aborted stage replayed: %+v %v", rep, err)
	}
}

func TestDeterministicRepeatedRecovery(t *testing.T) {
	dir := newTestDir(t)
	c, _ := OpenCoordinator(dir, nil)
	for i := 0; i < 3; i++ {
		stage := "seg-" + string(rune('0'+i))
		if err := c.CommitStage(stage, map[string][]byte{"d.bin": {byte(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	c.Close()
	first, err := Recover(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Recover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Artifacts) != len(second.Artifacts) {
		t.Fatal("recovery result length differs across runs")
	}
	for k, v := range first.Artifacts {
		if string(second.Artifacts[k]) != string(v) {
			t.Fatalf("artifact %s differs across recoveries", k)
		}
	}
}
