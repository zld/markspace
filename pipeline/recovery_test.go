package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func testStages(ran *map[string]int) []Stage {
	mk := func(name string) Stage {
		return Stage{Name: name, Run: func(deps map[string]json.RawMessage) (any, error) {
			(*ran)[name]++
			return map[string]string{"stage": name}, nil
		}}
	}
	return []Stage{mk("ingest"), mk("timeline"), mk("render"), mk("manifest")}
}

func newRunner(t *testing.T, dir, input, params string) *Runner {
	t.Helper()
	store, err := NewStore(filepath.Join(dir, "checkpoints"))
	if err != nil {
		t.Fatal(err)
	}
	return &Runner{Store: store, InputDigest: input, ParamsDigest: params}
}

func TestResumeFromLastValidStage(t *testing.T) {
	dir := t.TempDir()
	ran := map[string]int{}
	stages := testStages(&ran)
	// crash phase: only the first two stages complete
	r := newRunner(t, dir, "in1", "p1")
	if _, err := r.Run(stages[:2]); err != nil {
		t.Fatal(err)
	}
	// resume phase: full run resumes at render
	out, err := r.Run(stages)
	if err != nil {
		t.Fatal(err)
	}
	if out.ResumeFrom != "render" {
		t.Fatalf("resume_from=%s want render", out.ResumeFrom)
	}
	if ran["ingest"] != 1 || ran["timeline"] != 1 || ran["render"] != 1 || ran["manifest"] != 1 {
		t.Fatalf("stage run counts=%v", ran)
	}
	// a third run reuses everything
	out2, err := r.Run(stages)
	if err != nil {
		t.Fatal(err)
	}
	if out2.ResumeFrom != "none" {
		t.Fatalf("resume_from=%s want none", out2.ResumeFrom)
	}
	if out2.Provenance.ChainDigest != out.Provenance.ChainDigest {
		t.Fatal("chain digest changed across idempotent runs")
	}
	if !out2.Provenance.Verify("in1", "p1") {
		t.Fatal("provenance verification failed")
	}
}

func TestCorruptCheckpointFallsBack(t *testing.T) {
	dir := t.TempDir()
	ran := map[string]int{}
	stages := testStages(&ran)
	r := newRunner(t, dir, "in1", "p1")
	if _, err := r.Run(stages); err != nil {
		t.Fatal(err)
	}
	clean := ran["render"]
	// corrupt the render checkpoint on disk
	cpPath := filepath.Join(dir, "checkpoints", "render.json")
	if err := os.WriteFile(cpPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := r.Run(stages)
	if err != nil {
		t.Fatal(err)
	}
	if out.ResumeFrom != "render" {
		t.Fatalf("resume_from=%s want render", out.ResumeFrom)
	}
	if ran["render"] != clean+1 {
		t.Fatal("render stage was not re-executed after corruption")
	}
	if ran["ingest"] != 1 || ran["timeline"] != 1 {
		t.Fatal("upstream stages re-executed despite valid checkpoints")
	}
	// downstream artifacts of the corrupt checkpoint must not be reused
	if ran["manifest"] != 2 {
		t.Fatal("manifest stage reused a downstream artifact of a corrupt checkpoint")
	}
}

func TestStaleCheckpointNotReused(t *testing.T) {
	dir := t.TempDir()
	ran := map[string]int{}
	stages := testStages(&ran)
	r1 := newRunner(t, dir, "in1", "p1")
	if _, err := r1.Run(stages); err != nil {
		t.Fatal(err)
	}
	// same store, different parameters: nothing may be reused
	r2 := newRunner(t, dir, "in1", "p2")
	out, err := r2.Run(stages)
	if err != nil {
		t.Fatal(err)
	}
	if out.ResumeFrom != "ingest" {
		t.Fatalf("resume_from=%s want ingest", out.ResumeFrom)
	}
	if ran["ingest"] != 2 || ran["manifest"] != 2 {
		t.Fatalf("stale checkpoints were reused: %v", ran)
	}
	// different input digest likewise
	r3 := newRunner(t, dir, "in2", "p2")
	out3, err := r3.Run(stages)
	if err != nil {
		t.Fatal(err)
	}
	if out3.ResumeFrom != "ingest" {
		t.Fatalf("resume_from=%s want ingest", out3.ResumeFrom)
	}
}

func TestDeterministicChainAcrossFreshRuns(t *testing.T) {
	digests := map[string]bool{}
	for i := 0; i < 2; i++ {
		dir := t.TempDir()
		ran := map[string]int{}
		r := newRunner(t, dir, "in1", "p1")
		out, err := r.Run(testStages(&ran))
		if err != nil {
			t.Fatal(err)
		}
		digests[out.Provenance.ChainDigest] = true
	}
	if len(digests) != 1 {
		t.Fatal("identical inputs produced different provenance chains")
	}
}

func TestCheckpointDigestTamper(t *testing.T) {
	dir := t.TempDir()
	ran := map[string]int{}
	stages := testStages(&ran)
	r := newRunner(t, dir, "in1", "p1")
	if _, err := r.Run(stages); err != nil {
		t.Fatal(err)
	}
	// tamper with the payload but keep valid JSON: digest must catch it
	cpPath := filepath.Join(dir, "checkpoints", "timeline.json")
	b, _ := os.ReadFile(cpPath)
	var cp Checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		t.Fatal(err)
	}
	cp.Payload = json.RawMessage(`{"stage":"evil"}`)
	tampered, _ := json.Marshal(&cp)
	if err := os.WriteFile(cpPath, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := r.Run(stages)
	if err != nil {
		t.Fatal(err)
	}
	if out.ResumeFrom != "timeline" {
		t.Fatalf("resume_from=%s want timeline", out.ResumeFrom)
	}
}
