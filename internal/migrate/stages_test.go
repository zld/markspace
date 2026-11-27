package migrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/assetpack/internal/fault"
)

func TestNewJournalHasFiveStages(t *testing.T) {
	j := NewJournal("rel")
	if len(j.Records) != 5 {
		t.Fatalf("journal has %d records", len(j.Records))
	}
	if j.CompletedCount() != 0 || j.AllDone() {
		t.Fatal("fresh journal is not empty")
	}
	if j.ResumeFrom() != StageParse {
		t.Fatalf("fresh journal resumes at %s", j.ResumeFrom())
	}
	for _, s := range Order {
		r, ok := j.Get(s)
		if !ok || r.Status != StatusPending || r.Offset != -1 {
			t.Fatalf("record for %s wrong: %+v", s, r)
		}
	}
}

func TestJournalProgressAndResume(t *testing.T) {
	j := NewJournal("rel")
	j.Set(StageParse, StatusDone, 0, 0, "3 assets", nil)
	j.Set(StageAudio, StatusDone, 0, 192000, "", nil)
	if j.ResumeFrom() != StageStageManifest {
		t.Fatalf("resume from %s", j.ResumeFrom())
	}
	// A failure pins the resume point to the failed stage and keeps the
	// position needed to restart there.
	f := fault.New(fault.StageManifest, fault.KindInjected, "halt").AtOffset(752).Recover(true)
	j.Set(StageStageManifest, StatusFailed, 752, 96000, "injected", f)
	rec, _ := j.Get(StageStageManifest)
	if rec.Offset != 752 || rec.Attempts != 1 {
		t.Fatalf("failure record %+v", rec)
	}
	last, ok := j.LastFailure()
	if !ok || last.Stage != StageStageManifest {
		t.Fatalf("last failure %+v", last)
	}
	if j.ResumeFrom() != StageStageManifest {
		t.Fatalf("resume after failure is %s", j.ResumeFrom())
	}
}

func TestJournalSummaryOrder(t *testing.T) {
	j := NewJournal("rel")
	lines := j.Summary()
	if len(lines) != 5 {
		t.Fatalf("summary lines %d", len(lines))
	}
	for i, s := range Order {
		if !strings.HasPrefix(lines[i], string(s)) {
			t.Fatalf("line %d = %q, want prefix %s", i, lines[i], s)
		}
	}
}

func TestJournalPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	j := NewJournal("rel")
	j.Set(StageParse, StatusDone, 0, 0, "ok", nil)
	j.Set(StageAudio, StatusFailed, 188, 4800, "gap", fault.New(fault.StageAudio, fault.KindAudioGap, "gap").AtSample(4800))
	if err := j.Persist(dir); err != nil {
		t.Fatal(err)
	}
	back, err := LoadJournal(dir, "rel")
	if err != nil {
		t.Fatal(err)
	}
	if back.CompletedCount() != 1 || back.ResumeFrom() != StageAudio {
		t.Fatalf("round trip lost progress: %+v", back)
	}
	fail, _ := back.Get(StageAudio)
	if fail.Fault == nil || fail.Fault.Kind != fault.KindAudioGap || fail.Sample != 4800 {
		t.Fatalf("fault not persisted: %+v", fail)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

func TestLoadJournalMissingCreatesFresh(t *testing.T) {
	j, err := LoadJournal(t.TempDir(), "rel")
	if err != nil {
		t.Fatal(err)
	}
	if j.Release != "rel" || len(j.Records) != 5 {
		t.Fatalf("fresh load: %+v", j)
	}
}

func TestLoadJournalRepairsMissingStages(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stages.json"), []byte(`{"release":"rel","records":[{"stage":"commit","status":"done","offset":0,"sample":0}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	j, err := LoadJournal(dir, "rel")
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Records) != 5 {
		t.Fatalf("stages not repaired: %d", len(j.Records))
	}
	if j.ResumeFrom() != StageParse {
		t.Fatalf("resume from %s", j.ResumeFrom())
	}
}

func TestLoadJournalCorrupt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stages.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadJournal(dir, "rel"); err == nil {
		t.Fatal("corrupt journal accepted")
	}
}

func TestFailAfter(t *testing.T) {
	for _, s := range Order {
		got, err := FailAfter(string(s))
		if err != nil || got != s {
			t.Fatalf("FailAfter(%s) = %s, %v", s, got, err)
		}
	}
	if _, err := FailAfter(""); err != nil {
		t.Fatal("empty name should mean no failure")
	}
	_, err := FailAfter("nope")
	var f *fault.Fault
	if !errors.As(err, &f) || f.Kind != fault.KindInvalidArg {
		t.Fatalf("unknown stage accepted: %v", err)
	}
}

func TestAllDone(t *testing.T) {
	j := NewJournal("rel")
	for _, s := range Order {
		j.Set(s, StatusDone, 0, 0, "", nil)
	}
	if !j.AllDone() || j.CompletedCount() != 5 {
		t.Fatal("journal not complete")
	}
}
