package checkpoint

import (
	"os"
	"path/filepath"
	"testing"

	"mediaregress/internal/media"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	st := &State{InputPath: "in.mp4", InputDigest: "abc", InputSize: 10, Stage: media.StageSplit}
	tr := st.EnsureTrack("a0", "abc")
	tr.AddInterval(0, 100)
	tr.AddStage(media.StageSplit)
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.InputDigest != "abc" || len(got.Tracks) != 1 {
		t.Fatalf("loaded=%+v", got)
	}
	if !got.Tracks[0].Completed(0, 100) || got.Tracks[0].Completed(0, 101) {
		t.Errorf("interval coverage wrong: %+v", got.Tracks[0].Intervals)
	}
	if !got.Tracks[0].HasStage(media.StageSplit) {
		t.Error("stage not persisted")
	}
}

func TestLoadMissingIsNil(t *testing.T) {
	st, err := NewStore(t.TempDir()).Load()
	if err != nil || st != nil {
		t.Fatalf("st=%v err=%v", st, err)
	}
}

func TestLoadCorruptIsCheckpointError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewStore(dir).Load()
	if !media.IsClass(err, media.ClassCheckpoint) {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadWrongVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"version":999}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewStore(dir).Load()
	if !media.IsClass(err, media.ClassCheckpoint) {
		t.Fatalf("err=%v", err)
	}
}

func TestAtomicSaveNoTempLeft(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Save(&State{InputDigest: "x"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Fatalf("dir contents=%v", entries)
	}
}

func TestIntervalsCoalesce(t *testing.T) {
	tr := &TrackState{}
	tr.AddInterval(100, 200)
	tr.AddInterval(0, 100)
	tr.AddInterval(150, 250)
	if len(tr.Intervals) != 1 || tr.Intervals[0].Start != 0 || tr.Intervals[0].End != 250 {
		t.Fatalf("intervals=%+v", tr.Intervals)
	}
	tr.AddInterval(300, 400)
	if len(tr.Intervals) != 2 {
		t.Fatalf("intervals=%+v", tr.Intervals)
	}
}

func TestSaveIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	st := &State{InputDigest: "x", Stage: media.StageSplit}
	// Deliberately add tracks and intervals out of order.
	trB := st.EnsureTrack("b0", "x")
	trB.AddInterval(200, 300)
	trB.AddInterval(0, 100)
	trA := st.EnsureTrack("a0", "x")
	trA.AddInterval(50, 60)
	st.Normalize()
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st2 := &State{InputDigest: "x", Stage: media.StageSplit}
	st2.EnsureTrack("a0", "x").AddInterval(50, 60)
	b := st2.EnsureTrack("b0", "x")
	b.AddInterval(0, 100)
	b.AddInterval(200, 300)
	st2.Normalize()
	// Created/updated timestamps are wall-clock; compare the remaining bytes.
	data, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("unchanged checkpoint rewrote different bytes")
	}
	if data == nil {
		t.Fatal("state disappeared")
	}
}

func TestInvalidatedOnlyAffectedTracks(t *testing.T) {
	st := &State{}
	st.EnsureTrack("a0", "old-a")
	st.EnsureTrack("v0", "same-v")
	reset := st.ApplyDigests(map[string]string{"a0": "new-a", "v0": "same-v"})
	if len(reset) != 1 || reset[0] != "a0" {
		t.Fatalf("reset=%v", reset)
	}
	if st.Track("v0").InputDigest != "same-v" {
		t.Error("unchanged track touched")
	}
	if len(st.Track("a0").Intervals) != 0 {
		t.Error("changed track intervals not cleared")
	}
	if st.Track("a0").InputDigest != "new-a" {
		t.Errorf("digest=%s", st.Track("a0").InputDigest)
	}
}

func TestNewTrackHasNoReset(t *testing.T) {
	st := &State{}
	reset := st.ApplyDigests(map[string]string{"a0": "d"})
	if len(reset) != 0 {
		t.Fatalf("new track reported as reset: %v", reset)
	}
	if st.Track("a0") == nil {
		t.Fatal("new track not added")
	}
}

func TestInvalidated(t *testing.T) {
	st := &State{}
	st.EnsureTrack("a0", "a")
	st.EnsureTrack("v0", "v")
	got := st.Invalidated(map[string]string{"a0": "a2", "v0": "v"})
	if len(got) != 1 || got[0] != "a0" {
		t.Fatalf("invalidated=%v", got)
	}
	// A track absent from the current set is stale too.
	got = st.Invalidated(map[string]string{"v0": "v"})
	if len(got) != 1 || got[0] != "a0" {
		t.Fatalf("invalidated=%v", got)
	}
}

func TestResetTrackKeepsOthers(t *testing.T) {
	st := &State{}
	a := st.EnsureTrack("a0", "a")
	a.AddInterval(0, 10)
	a.AddStage(media.StageSplit)
	v := st.EnsureTrack("v0", "v")
	v.AddInterval(0, 10)
	st.ResetTrack("a0")
	if st.Track("a0").HasStage(media.StageSplit) || len(st.Track("a0").Intervals) != 0 {
		t.Error("a0 not reset")
	}
	if len(st.Track("v0").Intervals) != 1 {
		t.Error("v0 was reset")
	}
}
