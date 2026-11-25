package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSelfTestEndToEnd(t *testing.T) {
	dir := t.TempDir()
	rep, err := runSelfTest(dir, "test/self")
	if err != nil {
		t.Fatalf("self-test: %v", err)
	}
	if !rep.Passed {
		for _, c := range rep.Checks {
			t.Logf("FAILED check: %s: %s", c.Name, c.Detail)
		}
		t.Fatal("self-test assertions failed")
	}
	for _, c := range rep.Checks {
		if !c.OK {
			t.Fatalf("check %q failed: %s", c.Name, c.Detail)
		}
	}
	if rep.Normal.ManifestHash == "" {
		t.Fatal("normal manifest hash missing")
	}
	if rep.Normal.PacketsLost != 0 {
		t.Fatalf("normal scenario lost %d packets, want 0", rep.Normal.PacketsLost)
	}
	if rep.Damaged.PacketsLost == 0 {
		t.Fatal("damaged scenario should report lost packets")
	}
	if len(rep.Damaged.DamagedOffsets) == 0 {
		t.Fatal("damaged scenario should report a damaged byte offset")
	}
	if rep.Recovery.ManifestHash != rep.Recovery.UninterruptedHash {
		t.Fatalf("recovery hash %s != uninterrupted %s",
			rep.Recovery.ManifestHash, rep.Recovery.UninterruptedHash)
	}
	if rep.Recovery.ResumeStage == "" {
		t.Fatal("recovery should report the resume stage")
	}
	for _, e := range rep.Normal.Events {
		if e.Damaged {
			t.Fatalf("normal event %q marked damaged", e.Name)
		}
	}
}

func TestReportIsStableJSON(t *testing.T) {
	dir := t.TempDir()
	rep, err := runSelfTest(dir, "test/stable")
	if err != nil {
		t.Fatal(err)
	}
	var a, b bytes.Buffer
	if err := rep.emit(&a); err != nil {
		t.Fatal(err)
	}
	if err := rep.emit(&b); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("report encoding is not stable")
	}
	var decoded map[string]any
	if err := json.Unmarshal(a.Bytes(), &decoded); err != nil {
		t.Fatalf("report is not valid JSON: %v", err)
	}
	for _, key := range []string{"normal", "damaged", "recovery", "checks", "passed"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("report missing %q", key)
		}
	}
}

func TestResumeRefusesChangedInput(t *testing.T) {
	dir := t.TempDir()
	fs, err := buildFixtures(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := RunConfig{
		TaskKey:   "test/resume",
		WAVPath:   fs.NormalWAV,
		MP4Path:   fs.NormalMP4,
		Events:    eventSet(),
		RTPTrace:  fs.CleanTrace,
		OutDir:    filepath.Join(dir, "out", "a"),
		StateDir:  filepath.Join(dir, "state"),
		CkptDir:   filepath.Join(dir, "ckpt"),
		ClockRate: videoRate,
		StopAfter: "scan",
	}
	if _, err := Run(cfg); err != nil {
		t.Fatal(err)
	}
	// Change the media after a partial run; resuming must refuse.
	cfg.MP4Path = fs.CutMP4
	if _, err := Run(cfg); err == nil {
		t.Fatal("resume against changed media should fail")
	}
}

func TestByteIdenticalManifestAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	fs, err := buildFixtures(dir)
	if err != nil {
		t.Fatal(err)
	}
	run := func(tag string) []byte {
		cfg := RunConfig{
			TaskKey:   "test/deterministic",
			WAVPath:   fs.NormalWAV,
			MP4Path:   fs.NormalMP4,
			Events:    eventSet(),
			RTPTrace:  fs.CleanTrace,
			OutDir:    filepath.Join(dir, "out", tag),
			StateDir:  filepath.Join(dir, "state-"+tag),
			CkptDir:   filepath.Join(dir, "ckpt-"+tag),
			ClockRate: videoRate,
		}
		res, err := Run(cfg)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(res.ManifestPath)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if a, b := run("one"), run("two"); !bytes.Equal(a, b) {
		t.Fatalf("manifests differ between identical runs\n%s\n%s", a, b)
	}
}
