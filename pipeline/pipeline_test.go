package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"streamcheck/core"
)

func testEngine(t *testing.T, ran *[]string) *Engine {
	t.Helper()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "in.bin"), []byte("input"), 0o644); err != nil {
		t.Fatal(err)
	}
	mk := func(name string) Stage {
		return Stage{Name: name, Run: func(a map[string]string) (string, error) {
			*ran = append(*ran, name)
			return `{"stage":"` + name + `"}`, nil
		}}
	}
	return &Engine{
		Work:   work,
		Inputs: []string{"in.bin"},
		Stages: []Stage{mk("manifest"), mk("packet"), mk("inspect"), mk("migrate")},
	}
}

func TestExecuteFull(t *testing.T) {
	var ran []string
	e := testEngine(t, &ran)
	digest, err := e.InputDigest()
	if err != nil {
		t.Fatal(err)
	}
	arts, err := e.Execute(0, nil, digest, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 4 || len(arts) != 4 {
		t.Fatalf("ran=%v arts=%v", ran, arts)
	}
	cp, err := LoadCheckpoint(e.Work)
	if err != nil {
		t.Fatal(err)
	}
	if cp.Stage != "migrate" || cp.InputDigest != digest {
		t.Fatalf("checkpoint: %+v", cp)
	}
}

func TestStopAfterAndResume(t *testing.T) {
	var ran []string
	e := testEngine(t, &ran)
	digest, _ := e.InputDigest()
	_, err := e.Execute(0, nil, digest, "inspect")
	if !errors.Is(err, core.ErrStop) {
		t.Fatalf("want ErrStop, got %v", err)
	}
	if len(ran) != 3 {
		t.Fatalf("ran=%v", ran)
	}
	if code := core.ExitCode(err); code != 75 {
		t.Fatalf("exit code %d", code)
	}
	// Resume continues at migrate with the same inputs.
	cp, err := LoadCheckpoint(e.Work)
	if err != nil {
		t.Fatal(err)
	}
	start, err := e.ResumePoint(cp)
	if err != nil {
		t.Fatal(err)
	}
	if start != 3 {
		t.Fatalf("resume at %d", start)
	}
	arts, err := e.Execute(start, cp.Artifacts, digest, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 4 || ran[3] != "migrate" || len(arts) != 4 {
		t.Fatalf("ran=%v arts=%v", ran, arts)
	}
}

func TestResumeRejectsChangedInputs(t *testing.T) {
	var ran []string
	e := testEngine(t, &ran)
	digest, _ := e.InputDigest()
	if _, err := e.Execute(0, nil, digest, "inspect"); err == nil {
		t.Fatal("expected stop")
	}
	if err := os.WriteFile(filepath.Join(e.Work, "in.bin"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	cp, _ := LoadCheckpoint(e.Work)
	if _, err := e.ResumePoint(cp); err == nil {
		t.Fatal("changed inputs must refuse resume")
	}
}

func TestCorruptCheckpointRejected(t *testing.T) {
	var ran []string
	e := testEngine(t, &ran)
	digest, _ := e.InputDigest()
	if _, err := e.Execute(0, nil, digest, "manifest"); err == nil {
		t.Fatal("expected stop")
	}
	path := filepath.Join(e.Work, CheckpointFile)
	data, _ := os.ReadFile(path)
	// Flip a byte inside the artifacts section.
	data[len(data)-40] ^= 0xff
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckpoint(e.Work); err == nil {
		t.Fatal("corrupt checkpoint must be rejected")
	}
}

func TestMissingCheckpoint(t *testing.T) {
	if _, err := LoadCheckpoint(t.TempDir()); err == nil {
		t.Fatal("missing checkpoint must fail")
	}
}
