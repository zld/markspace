package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediad/internal/clock"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func entry(stage Stage, k Key, payload string) *Entry {
	return &Entry{Stage: stage, InputSHA: k.InputSHA, ParamSHA: k.ParamSHA, StageVer: k.StageVer,
		OutputSHA: Fingerprint(payload), Payload: []byte(`"` + payload + `"`)}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := openStore(t)
	k := Key{InputSHA: "in", ParamSHA: "par", StageVer: 1}
	e := entry(StageScan, k, "scan-out")
	if err := s.Save(e); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(StageScan)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Match(k) {
		t.Fatalf("entry does not match its key: %s", Mismatch(got, k))
	}
	if got.OutputSHA != Fingerprint("scan-out") {
		t.Fatal("output digest not preserved")
	}
}

func TestResumeAllStages(t *testing.T) {
	s := openStore(t)
	keys := map[Stage]Key{}
	for _, st := range Order() {
		k := Key{InputSHA: "in", ParamSHA: "par", StageVer: 1}
		keys[st] = k
		if err := s.Save(entry(st, k, string(st))); err != nil {
			t.Fatal(err)
		}
	}
	last, run, notes, err := s.ResumePlan(keys)
	if err != nil {
		t.Fatal(err)
	}
	if last != StageVerify || len(run) != 0 {
		t.Fatalf("last=%s run=%v notes=%v", last, run, notes)
	}
}

func TestInputChangeInvalidatesFromStage(t *testing.T) {
	s := openStore(t)
	base := Key{InputSHA: "in", ParamSHA: "par", StageVer: 1}
	for _, st := range Order() {
		if err := s.Save(entry(st, base, string(st))); err != nil {
			t.Fatal(err)
		}
	}
	// A changed input at index invalidates index and verify only.
	keys := map[Stage]Key{}
	for _, st := range Order() {
		keys[st] = base
	}
	keys[StageIndex] = Key{InputSHA: "changed", ParamSHA: "par", StageVer: 1}
	last, run, notes, err := s.ResumePlan(keys)
	if err != nil {
		t.Fatal(err)
	}
	if last != StageRepair {
		t.Fatalf("last valid=%s want repair", last)
	}
	if len(run) != 2 || run[0] != StageIndex || run[1] != StageVerify {
		t.Fatalf("run=%v", run)
	}
	if len(notes) == 0 || !strings.Contains(notes[0], "input digest changed") {
		t.Fatalf("notes=%v", notes)
	}
	// Scan and repair survive; index/verify are removed.
	present, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(present) != 2 || present[0] != StageScan || present[1] != StageRepair {
		t.Fatalf("present=%v", present)
	}
}

func TestCorruptCheckpointRecomputed(t *testing.T) {
	s := openStore(t)
	keys := map[Stage]Key{}
	base := Key{InputSHA: "in", ParamSHA: "par", StageVer: 1}
	for _, st := range Order() {
		keys[st] = base
		if err := s.Save(entry(st, base, string(st))); err != nil {
			t.Fatal(err)
		}
	}
	// Truncate the repair checkpoint, leaving invalid JSON.
	if err := os.WriteFile(filepath.Join(s.Dir(), "repair.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	last, run, notes, err := s.ResumePlan(keys)
	if err != nil {
		t.Fatal(err)
	}
	if last != StageScan {
		t.Fatalf("last=%s want scan", last)
	}
	if len(run) != 3 {
		t.Fatalf("run=%v", run)
	}
	if len(notes) == 0 || !strings.Contains(notes[0], "malformed") {
		t.Fatalf("notes=%v", notes)
	}
	// The corrupt entry and all later ones must be gone.
	if _, err := os.Stat(filepath.Join(s.Dir(), "repair.json")); !os.IsNotExist(err) {
		t.Fatalf("corrupt checkpoint not removed: %v", err)
	}
}

func TestStageVersionChangeInvalidates(t *testing.T) {
	s := openStore(t)
	k := Key{InputSHA: "in", ParamSHA: "par", StageVer: 1}
	if err := s.Save(entry(StageScan, k, "scan")); err != nil {
		t.Fatal(err)
	}
	newKey := Key{InputSHA: "in", ParamSHA: "par", StageVer: 2}
	last, run, _, err := s.ResumePlan(map[Stage]Key{StageScan: newKey})
	if err != nil {
		t.Fatal(err)
	}
	if last != "" || len(run) == 0 {
		t.Fatalf("version bump must invalidate: last=%s run=%v", last, run)
	}
}

func TestMismatchedStageFileIsCorrupt(t *testing.T) {
	s := openStore(t)
	// Write a scan.json whose internal stage says repair.
	raw := `{"version":1,"stage":"repair","input_sha256":"in","param_sha256":"par","stage_version":1,"output_sha256":"x","status":"complete"}`
	if err := os.WriteFile(filepath.Join(s.Dir(), "scan.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load(StageScan)
	pe, ok := clock.As(err)
	if !ok || pe.Code != clock.CodeCheckpointCorrupt {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestFingerprintStability(t *testing.T) {
	a := Fingerprint("a", "bc")
	b := Fingerprint("a", "bc")
	if a != b {
		t.Fatal("fingerprint unstable")
	}
	if Fingerprint("ab", "c") == a {
		t.Fatal("length prefix not applied")
	}
	p1, err := ParamsFingerprint(struct {
		A int
		B string
	}{1, "x"})
	if err != nil {
		t.Fatal(err)
	}
	p2, _ := ParamsFingerprint(struct {
		A int
		B string
	}{1, "x"})
	if p1 != p2 {
		t.Fatal("params fingerprint unstable")
	}
}

func TestInvalidateUnknownStage(t *testing.T) {
	s := openStore(t)
	if err := s.Invalidate("bogus"); err == nil {
		t.Fatal("expected error")
	}
}
