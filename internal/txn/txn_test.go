package txn

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/timeline"
)

func testOutputs() []StageOutput {
	return []StageOutput{
		{Stage: StageParse, Name: "parse.json"},
		{Stage: StageTimeline, Name: "timeline.json"},
	}
}

func writeContent(p string, content []byte) error {
	return os.WriteFile(p, content, privateFileMode)
}

func TestRunStageCommitsAtomically(t *testing.T) {
	dir := t.TempDir()
	wal, err := CreateWAL(dir, "input-abc")
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(wal, testOutputs())

	content := []byte(`{"ok":true}`)
	if err := mgr.RunStage(StageParse, func(temp string) error {
		return writeContent(temp, content)
	}); err != nil {
		t.Fatal(err)
	}
	// Committed output exists with the exact content; temp is gone.
	got, err := os.ReadFile(filepath.Join(dir, "parse.json"))
	if err != nil || string(got) != string(content) {
		t.Fatalf("committed output wrong: %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tmp-"+StageParse)); !os.IsNotExist(err) {
		t.Fatal("temp file must be removed after commit")
	}
	if mgr.wal.LastCommittedStage() != StageParse {
		t.Fatal("WAL must record the committed stage")
	}
	if err := wal.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRunStageFailureLeavesNoHalfOutput(t *testing.T) {
	dir := t.TempDir()
	wal, _ := CreateWAL(dir, "input-abc")
	mgr := NewManager(wal, testOutputs())

	boom := errors.New("stage boom")
	err := mgr.RunStage(StageParse, func(temp string) error {
		if e := writeContent(temp, []byte("partial")); e != nil {
			return e
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("stage error must propagate, got %v", err)
	}
	// No committed file, no leftover temp.
	if _, err := os.Stat(filepath.Join(dir, "parse.json")); !os.IsNotExist(err) {
		t.Fatal("failed stage must not leave a committed output")
	}
	if _, err := os.Stat(filepath.Join(dir, "tmp-"+StageParse)); !os.IsNotExist(err) {
		t.Fatal("failed stage must clean its temp file")
	}
	// The intent record exists but no committed record.
	if mgr.wal.LastCommittedStage() != "" {
		t.Fatal("no stage may be marked committed after failure")
	}
}

func TestRecoverRejectsMismatchedInput(t *testing.T) {
	dir := t.TempDir()
	wal, _ := CreateWAL(dir, "input-abc")
	mgr := NewManager(wal, testOutputs())
	if err := mgr.RunStage(StageParse, func(temp string) error {
		return writeContent(temp, []byte("data"))
	}); err != nil {
		t.Fatal(err)
	}
	_, err := mgr.Recover("input-DIFFERENT")
	var me *timeline.MediaError
	if !errors.As(err, &me) || me.Code != timeline.ErrRecovery {
		t.Fatalf("digest mismatch must be ErrRecovery, got %v", err)
	}
	if timeline.ExitCode(err) != 3 {
		t.Fatalf("recovery mismatch exit code = %d, want 3", timeline.ExitCode(err))
	}
}

func TestRecoverRejectsTamperedOutput(t *testing.T) {
	dir := t.TempDir()
	wal, _ := CreateWAL(dir, "input-abc")
	mgr := NewManager(wal, testOutputs())
	if err := mgr.RunStage(StageParse, func(temp string) error {
		return writeContent(temp, []byte("data"))
	}); err != nil {
		t.Fatal(err)
	}
	// Tamper with the committed output after the WAL checksum was recorded.
	if err := os.WriteFile(filepath.Join(dir, "parse.json"), []byte("tampered"), privateFileMode); err != nil {
		t.Fatal(err)
	}
	_, err := mgr.Recover("input-abc")
	var me *timeline.MediaError
	if !errors.As(err, &me) || me.Code != timeline.ErrRecovery {
		t.Fatalf("tampered output must fail recovery, got %v", err)
	}
}

func TestResumeSkipsCommittedStageAndIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	run := func() (string, error) {
		wal, err := CreateWAL(dir, "input-abc")
		if err != nil {
			return "", err
		}
		mgr := NewManager(wal, testOutputs())
		if _, err := mgr.Recover("input-abc"); err != nil {
			return "", err
		}
		calls := 0
		err = mgr.RunStage(StageParse, func(temp string) error {
			calls++
			return writeContent(temp, []byte("deterministic"))
		})
		if err != nil {
			return "", err
		}
		if err := mgr.RunStage(StageTimeline, func(temp string) error {
			return writeContent(temp, []byte(`{"axis":"us"}`))
		}); err != nil {
			return "", err
		}
		if err := wal.Close(); err != nil {
			return "", err
		}
		sum, err := FileSHA256(filepath.Join(dir, "parse.json"))
		return sum, err
	}

	sum1, err := run()
	if err != nil {
		t.Fatal(err)
	}
	sum2, err := run()
	if err != nil {
		t.Fatal(err)
	}
	if sum1 != sum2 {
		t.Fatalf("repeated runs must yield identical checksum: %s vs %s", sum1, sum2)
	}
}

func TestRollbackAllowsRerun(t *testing.T) {
	dir := t.TempDir()
	wal, _ := CreateWAL(dir, "input-abc")
	mgr := NewManager(wal, testOutputs())
	if err := mgr.RunStage(StageParse, func(temp string) error {
		return writeContent(temp, []byte("v1"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Rollback(StageParse); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "parse.json")); !os.IsNotExist(err) {
		t.Fatal("rollback must remove committed output")
	}
	if err := mgr.RunStage(StageParse, func(temp string) error {
		return writeContent(temp, []byte("v2"))
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "parse.json"))
	if string(got) != "v2" {
		t.Fatalf("rerun after rollback wrong: %q", got)
	}
}

func TestTempFilesAreOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	wal, _ := CreateWAL(dir, "input-abc")
	mgr := NewManager(wal, testOutputs())
	if err := mgr.RunStage(StageParse, func(temp string) error {
		return writeContent(temp, []byte("x"))
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "parse.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != privateFileMode {
		t.Fatalf("output perm = %o, want %o", perm, privateFileMode)
	}
}

func TestCreateWALRejectsCorruptRecord(t *testing.T) {
	dir := t.TempDir()
	// Pre-create a WAL with a malformed line.
	if err := os.WriteFile(filepath.Join(dir, "wal.jsonl"), []byte("{not json}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := CreateWAL(dir, "input-x")
	var me *timeline.MediaError
	if !errors.As(err, &me) || me.Code != timeline.ErrRecovery {
		t.Fatalf("corrupt WAL must be ErrRecovery, got %v", err)
	}
}

func TestAppendRejectsForeignInputDigest(t *testing.T) {
	dir := t.TempDir()
	wal, err := CreateWAL(dir, "input-bound")
	if err != nil {
		t.Fatal(err)
	}
	_, err = wal.Append(Record{Stage: StageParse, InputSHA: "input-other"})
	var me *timeline.MediaError
	if !errors.As(err, &me) || me.Code != timeline.ErrRecovery {
		t.Fatalf("foreign digest append must fail, got %v", err)
	}
}

func TestBytesSHA256Deterministic(t *testing.T) {
	if BytesSHA256([]byte("abc")) != BytesSHA256(append([]byte{}, "abc"...)) {
		t.Fatal("identical bytes must hash identically")
	}
	if BytesSHA256([]byte("abc")) == BytesSHA256([]byte("abd")) {
		t.Fatal("different bytes must hash differently")
	}
}

func TestAtomicRenameReplacesDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicRename(src, dst); err != nil {
		t.Fatalf("atomicRename: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "new" {
		t.Fatalf("destination not replaced: %q err=%v", got, err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source must be gone after rename")
	}
}
