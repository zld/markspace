package provenance

import (
	"encoding/json"
	"testing"

	"mediaregress/internal/media"
)

func newRecord() *Record {
	return New(
		InputSummary{Path: "in.mp4", Size: 100, SHA256: "abc", TrackCount: 2},
		[]StageRecord{
			{Stage: media.StageParse, Version: "trackparse/1"},
			{Stage: media.StageSplit, Version: "mediaregress/1"},
		},
		[]string{"a0", "v0"}, nil, nil,
	)
}

func TestSealIsStable(t *testing.T) {
	a := newRecord()
	b := newRecord()
	da, err := a.Seal()
	if err != nil {
		t.Fatal(err)
	}
	db, err := b.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if da != db {
		t.Fatalf("seal differs: %s vs %s", da, db)
	}
	if err := a.Verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestSealChangesWithInput(t *testing.T) {
	a := newRecord()
	da, _ := a.Seal()
	b := newRecord()
	b.Input.SHA256 = "different"
	db, _ := b.Seal()
	if da == db {
		t.Fatal("input digest not covered by seal")
	}
}

func TestSealChangesWithOutput(t *testing.T) {
	a := newRecord()
	a.SetOutput("out.json", "outdigest", 10)
	da, _ := a.Seal()
	b := newRecord()
	b.SetOutput("out.json", "other", 10)
	db, _ := b.Seal()
	if da == db {
		t.Fatal("output digest not covered by seal")
	}
}

func TestVerifyDetectsTamper(t *testing.T) {
	a := newRecord()
	if _, err := a.Seal(); err != nil {
		t.Fatal(err)
	}
	a.TrackIDs = append(a.TrackIDs, "sneaky")
	if err := a.Verify(); !media.IsClass(err, media.ClassOutputMismatch) {
		t.Fatalf("tamper not detected: %v", err)
	}
}

func TestVerifyUnsealed(t *testing.T) {
	if err := newRecord().Verify(); !media.IsClass(err, media.ClassOutputMismatch) {
		t.Fatalf("err=%v", err)
	}
}

func TestIncompleteFlagSurvivesJSON(t *testing.T) {
	r := newRecord()
	r.MarkIncomplete()
	b, err := r.MarshalIndent()
	if err != nil {
		t.Fatal(err)
	}
	var back Record
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Complete {
		t.Fatal("incomplete record round-tripped as complete")
	}
	if back.ProcessingVersion != ProcessingVersion {
		t.Errorf("version=%s", back.ProcessingVersion)
	}
}

func TestEveryTrackRecorded(t *testing.T) {
	r := newRecord()
	r.RejectedTracks = []string{"v1"}
	r.TruncatedTracks = []string{"a0"}
	b, _ := json.Marshal(r)
	var m map[string]any
	json.Unmarshal(b, &m)
	for _, key := range []string{"track_ids", "rejected_tracks", "truncated_tracks", "stages"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing %s", key)
		}
	}
}

func TestStagesNotAliased(t *testing.T) {
	stages := []StageRecord{{Stage: media.StageParse, Version: "v"}}
	r := New(InputSummary{}, stages, nil, nil, nil)
	stages[0].Version = "mutated"
	if r.Stages[0].Version != "v" {
		t.Fatal("stages slice aliased")
	}
}
