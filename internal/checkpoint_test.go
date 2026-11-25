package continuity

import (
	"os"
	"path/filepath"
	"testing"
)

func checkpointSetup(t *testing.T) (*TrackMap, *Timeline) {
	t.Helper()
	init, err := ParseInit("init.mp4", testInit())
	if err != nil {
		t.Fatal(err)
	}
	tm, err := BuildTrackMap([]TrackSource{{Label: "main", Init: init}})
	if err != nil {
		t.Fatal(err)
	}
	tl, err := NewTimeline(tm, DefaultTimelineConfig())
	if err != nil {
		t.Fatal(err)
	}
	return tm, tl
}

func addVideoRun(tl *Timeline, from, count int) {
	for i := from; i < from+count; i++ {
		s := sampleAt(i, Ticks(i*3000), 3000)
		if err := tl.Add("main", s); err != nil {
			panic(err)
		}
	}
}

func TestCheckpointRoundTrip(t *testing.T) {
	tm, tl := checkpointSetup(t)
	addVideoRun(tl, 0, 4)
	cp, err := NewCheckpoint(CheckpointConfig{RunID: "run-1", TrackMapHash: tm.Hash()}, tl)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state", "checkpoint.json")
	if err := SaveCheckpoint(path, cp); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCheckpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.OutputDigest != cp.OutputDigest || len(loaded.Samples) != 4 {
		t.Fatalf("loaded=%+v", loaded)
	}
	restored, err := RestoreCheckpoint(loaded, tm, DefaultTimelineConfig())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	prog, err := restored.Recompute()
	if err != nil {
		t.Fatal(err)
	}
	if len(prog.Samples) != 4 || prog.CommittedEnd != tl.Watermark() {
		t.Fatalf("restored program=%+v watermark=%d", prog.Stats, restored.Watermark())
	}
}

func TestCheckpointResumeAppendsDeterministically(t *testing.T) {
	tm, tl := checkpointSetup(t)
	addVideoRun(tl, 0, 3)
	cp, err := NewCheckpoint(CheckpointConfig{TrackMapHash: tm.Hash()}, tl)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreCheckpoint(cp, tm, DefaultTimelineConfig())
	if err != nil {
		t.Fatal(err)
	}
	// Continue the same run on the restored timeline.
	addVideoRun(restored, 3, 3)
	// A clean full run must produce the identical output digest.
	_, fresh := checkpointSetup(t)
	addVideoRun(fresh, 0, 6)
	wantProg, err := fresh.Recompute()
	if err != nil {
		t.Fatal(err)
	}
	gotProg, err := restored.Recompute()
	if err != nil {
		t.Fatal(err)
	}
	gotCp, _ := NewCheckpoint(CheckpointConfig{TrackMapHash: tm.Hash()}, restored)
	wantCp, _ := NewCheckpoint(CheckpointConfig{TrackMapHash: tm.Hash()}, fresh)
	if gotCp.OutputDigest != wantCp.OutputDigest {
		t.Fatalf("resumed output digest %s differs from clean run %s", gotCp.OutputDigest, wantCp.OutputDigest)
	}
	if gotProg.Stats.Samples != wantProg.Stats.Samples {
		t.Fatalf("samples %d vs %d", gotProg.Stats.Samples, wantProg.Stats.Samples)
	}
}

func TestCheckpointMappingMismatch(t *testing.T) {
	tm, tl := checkpointSetup(t)
	addVideoRun(tl, 0, 2)
	cp, err := NewCheckpoint(CheckpointConfig{TrackMapHash: tm.Hash()}, tl)
	if err != nil {
		t.Fatal(err)
	}
	// A different source produces a different mapping hash.
	otherInit, err := ParseInit("init.mp4", testInit())
	if err != nil {
		t.Fatal(err)
	}
	otherMap, err := BuildTrackMap([]TrackSource{{Label: "other", Init: otherInit}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = RestoreCheckpoint(cp, otherMap, DefaultTimelineConfig())
	if err == nil || !IsKind(err, KindCheckpoint) {
		t.Fatalf("err=%v", err)
	}
}

func TestCheckpointCorruptPayloadDetected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.json")
	cp := &Checkpoint{Version: CheckpointVersion, OutputDigest: "deadbeef", Samples: nil}
	if err := SaveCheckpoint(path, cp); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckpoint(path); err == nil || !IsKind(err, KindCheckpoint) {
		t.Fatalf("err=%v", err)
	}
}

func TestCheckpointMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckpoint(path); err == nil || !IsKind(err, KindCheckpoint) {
		t.Fatalf("err=%v", err)
	}
}

func TestVerifyInputPrefixFirstConflict(t *testing.T) {
	tm, tl := checkpointSetup(t)
	addVideoRun(tl, 0, 3)
	cp, err := NewCheckpoint(CheckpointConfig{TrackMapHash: tm.Hash()}, tl)
	if err != nil {
		t.Fatal(err)
	}
	inputs := acceptedToSamples(tl.accepted)
	if idx, err := VerifyInputPrefix(cp, inputs); err != nil || idx != -1 {
		t.Fatalf("prefix should match: idx=%d err=%v", idx, err)
	}
	inputs[1].Data = pattern(200, 10)
	idx, err := VerifyInputPrefix(cp, inputs)
	if err == nil || idx != 1 {
		t.Fatalf("idx=%d err=%v", idx, err)
	}
	if loc := LocationOf(err); loc.Sample != inputs[1].Index {
		t.Fatalf("loc=%+v", loc)
	}
}

func TestCheckpointRejectsLateWindowChange(t *testing.T) {
	tm, tl := checkpointSetup(t)
	addVideoRun(tl, 0, 2)
	cp, err := NewCheckpoint(CheckpointConfig{TrackMapHash: tm.Hash()}, tl)
	if err != nil {
		t.Fatal(err)
	}
	other := DefaultTimelineConfig()
	other.LateWindow += 1000
	if _, err := RestoreCheckpoint(cp, tm, other); err == nil || !IsKind(err, KindCheckpoint) {
		t.Fatalf("err=%v", err)
	}
}

func TestCheckpointDeterministicJSON(t *testing.T) {
	tm, tl := checkpointSetup(t)
	addVideoRun(tl, 0, 3)
	a, err := NewCheckpoint(CheckpointConfig{TrackMapHash: tm.Hash()}, tl)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewCheckpoint(CheckpointConfig{TrackMapHash: tm.Hash()}, tl)
	ja, err := MarshalCheckpoint(a)
	if err != nil {
		t.Fatal(err)
	}
	jb, _ := MarshalCheckpoint(b)
	if string(ja) != string(jb) {
		t.Fatal("checkpoint JSON must be deterministic")
	}
}
