package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"replaycheck/internal/checkpoint"
	"replaycheck/internal/clip"
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

func TestSelfTestIsRepeatable(t *testing.T) {
	dir := t.TempDir()
	first, err := runSelfTest(dir, "test/repeat")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Passed {
		t.Fatal("first run failed")
	}
	second, err := runSelfTest(dir, "test/repeat")
	if err != nil {
		t.Fatalf("second run into the same directory: %v", err)
	}
	if !second.Passed {
		for _, c := range second.Checks {
			if !c.OK {
				t.Logf("FAILED: %s: %s", c.Name, c.Detail)
			}
		}
		t.Fatal("second run failed")
	}
	if first.Normal.ManifestHash != second.Normal.ManifestHash {
		t.Fatalf("manifest changed between runs: %s vs %s",
			first.Normal.ManifestHash, second.Normal.ManifestHash)
	}
	if second.Recovery.ResumeStage != "repair" {
		t.Fatalf("second run resumed at %q, want repair", second.Recovery.ResumeStage)
	}
}

func TestResumeRefusesMissingCheckpoint(t *testing.T) {
	dir := t.TempDir()
	fs, err := buildFixtures(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := RunConfig{
		TaskKey: "test/missing-ckpt", WAVPath: fs.NormalWAV, MP4Path: fs.NormalMP4,
		Events: eventSet(), RTPTrace: fs.CleanTrace, OutDir: filepath.Join(dir, "out", "m"),
		StateDir: filepath.Join(dir, "state"), CkptDir: filepath.Join(dir, "ckpt"),
		ClockRate: videoRate, StopAfter: "scan",
	}
	if _, err := Run(cfg); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.New(cfg.CkptDir).Remove(cfg.TaskKey, "scan"); err != nil {
		t.Fatal(err)
	}
	cfg.StopAfter = ""
	if _, err := Run(cfg); err == nil {
		t.Fatal("resume with a missing checkpoint should fail")
	}
}

func TestResumeRefusesStaleCheckpoint(t *testing.T) {
	dir := t.TempDir()
	fs, err := buildFixtures(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := RunConfig{
		TaskKey: "test/stale-ckpt", WAVPath: fs.NormalWAV, MP4Path: fs.NormalMP4,
		Events: eventSet(), RTPTrace: fs.CleanTrace, OutDir: filepath.Join(dir, "out", "s"),
		StateDir: filepath.Join(dir, "state"), CkptDir: filepath.Join(dir, "ckpt"),
		ClockRate: videoRate, StopAfter: "scan",
	}
	if _, err := Run(cfg); err != nil {
		t.Fatal(err)
	}
	ptr := checkpoint.New(cfg.CkptDir)
	if err := ptr.Write(cfg.TaskKey, "scan", "other-digest", map[string]int{"samples": 1}); err != nil {
		t.Fatal(err)
	}
	cfg.StopAfter = ""
	if _, err := Run(cfg); err == nil {
		t.Fatal("resume with a stale checkpoint should fail")
	}
}

func TestEmptyEventListMergesEmptyManifest(t *testing.T) {
	dir := t.TempDir()
	fs, err := buildFixtures(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := RunConfig{
		TaskKey: "test/empty", WAVPath: fs.NormalWAV, MP4Path: fs.NormalMP4,
		Events: nil, RTPTrace: fs.CleanTrace, OutDir: filepath.Join(dir, "out", "e"),
		StateDir: filepath.Join(dir, "state"), CkptDir: filepath.Join(dir, "ckpt"),
		ClockRate: videoRate,
	}
	res, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest == nil || len(res.Manifest.Clips) != 0 || res.Manifest.Hash == "" {
		t.Fatalf("manifest = %+v", res.Manifest)
	}
}

func TestEventEndingAtProgrammeEnd(t *testing.T) {
	dir := t.TempDir()
	fs, err := buildFixtures(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := RunConfig{
		TaskKey: "test/programme-end", WAVPath: fs.NormalWAV, MP4Path: fs.NormalMP4,
		Events:   []clip.Event{{Name: "tail", StartTick: 270000, EndTick: 360000}},
		RTPTrace: fs.CleanTrace, OutDir: filepath.Join(dir, "out", "p"),
		StateDir: filepath.Join(dir, "state"), CkptDir: filepath.Join(dir, "ckpt"),
		ClockRate: videoRate,
	}
	res, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Clips) != 1 {
		t.Fatalf("clips = %d", len(res.Clips))
	}
	c := res.Clips[0]
	if c.AudioFrames != 48000 {
		t.Fatalf("frames = %d, want 48000", c.AudioFrames)
	}
	if c.VideoLast != 119 {
		t.Fatalf("last sample = %d, want 119", c.VideoLast)
	}
}
