package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"transcode/internal/media"
	"transcode/internal/model"
	"transcode/internal/safeio"
)

func mkTask(n int, input string, fp string) *model.Task {
	t := &model.Task{ID: "job-1", InputPath: input, InputFingerprint: fp,
		DurationSec: 30, SegmentSec: 10, TargetKbps: 1000, MinKbps: 100,
		Segments: make([]*model.Segment, n)}
	for i := range t.Segments {
		t.Segments[i] = &model.Segment{Index: i, Status: model.StatusQueued,
			StartSec: float64(i) * 10, DurationSec: 10, TargetKbps: 1000, Feasible: true}
	}
	return t
}

func TestSaveLoadRoundTripAndCorruption(t *testing.T) {
	dir := t.TempDir()
	store, path, err := NewStore(dir, "job.json")
	if err != nil {
		t.Fatal(err)
	}
	cp := &Checkpoint{JobID: "job-1", InputFingerprint: "abc",
		Params: Params{InputPath: "in.vid", DurationSec: 30, SegmentSec: 10,
			TargetKbps: 1000, MinKbps: 100},
		Segments: []*model.Segment{{Index: 0, Status: model.StatusSucceeded}}}
	if err := store.Save(path, cp); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(path)
	if err != nil || got.JobID != "job-1" || got.SchemaVersion != SchemaVersion {
		t.Fatalf("round trip failed: %+v err=%v", got, err)
	}
	missing, err := store.Load(filepath.Join(dir, "nope.json"))
	if err != nil || missing != nil {
		t.Fatal("missing checkpoint must return nil, nil")
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(path); !errors.Is(err, ErrCorruptCheckpoint) {
		t.Fatalf("want ErrCorruptCheckpoint, got %v", err)
	}
}

func TestRecoverResumesAndRequeues(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.vid")
	out := filepath.Join(dir, "seg0.enc")
	v := &media.Video{Width: 2, Height: 2, FPS: 5, Frames: [][]byte{{1, 2, 3, 4}}}
	if err := media.WriteVideo(in, v); err != nil {
		t.Fatal(err)
	}
	e := &media.Encoded{Width: 2, Height: 2, FPS: 5, Quant: 4, Frames: v.Frames}
	if err := media.WriteEncoded(out, e, 128); err != nil {
		t.Fatal(err)
	}
	fp, _ := media.Fingerprint(in)
	task := mkTask(3, in, fp)
	cp := &Checkpoint{JobID: "job-1", InputFingerprint: fp,
		Params: Params{InputPath: in, DurationSec: 30, SegmentSec: 10, TargetKbps: 1000, MinKbps: 100},
		Segments: []*model.Segment{
			{Index: 0, Status: model.StatusSucceeded,
				Encode: &model.EncodeResult{OutputPath: out, Bytes: 128},
				Quality: &model.QualityResult{MeanPSNRDB: 40}},
			{Index: 1, Status: model.StatusRunning},
			{Index: 2, Status: model.StatusFailed, FailureNote: "boom"},
		}}
	outcome, err := Recover(cp, task, in)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Resumed || task.Segments[0].Status != model.StatusSucceeded ||
		task.Segments[0].Quality.MeanPSNRDB != 40 {
		t.Fatalf("verified segment not resumed: %+v", outcome)
	}
	if task.Segments[1].Status != model.StatusQueued || len(outcome.RequeuedRunning) != 1 {
		t.Fatal("running segment must be re-queued")
	}
	if task.Segments[2].Status != model.StatusQueued || len(outcome.RequeuedFailed) != 1 {
		t.Fatal("failed segment must be re-queued")
	}
}

func TestRecoverInvalidatesOnInputChangeAndMissingOutput(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.vid")
	out := filepath.Join(dir, "seg0.enc")
	v := &media.Video{Width: 2, Height: 2, FPS: 5, Frames: [][]byte{{1, 2, 3, 4}}}
	if err := media.WriteVideo(in, v); err != nil {
		t.Fatal(err)
	}
	e := &media.Encoded{Width: 2, Height: 2, FPS: 5, Quant: 4, Frames: v.Frames}
	if err := media.WriteEncoded(out, e, 128); err != nil {
		t.Fatal(err)
	}
	oldFP := "0000000000000000000000000000000000000000000000000000000000000000"
	task := mkTask(2, in, "new")
	cp := &Checkpoint{JobID: "job-1", InputFingerprint: oldFP,
		Params: Params{InputPath: in, DurationSec: 30, SegmentSec: 10, TargetKbps: 1000, MinKbps: 100},
		Segments: []*model.Segment{
			{Index: 0, Status: model.StatusSucceeded,
				Encode: &model.EncodeResult{OutputPath: out}},
			{Index: 1, Status: model.StatusSucceeded,
				Encode: &model.EncodeResult{OutputPath: filepath.Join(dir, "gone.enc")}},
		}}
	outcome, err := Recover(cp, task, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Invalidated) != 2 {
		t.Fatalf("both segments should invalidate, got %v", outcome.Invalidated)
	}
	for _, seg := range task.Segments {
		if seg.Status != model.StatusQueued || seg.Encode != nil {
			t.Fatal("invalidated segment must be re-queued without stale results")
		}
	}
}

func TestRecoverRejectsParameterDrift(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.vid")
	v := &media.Video{Width: 2, Height: 2, FPS: 5, Frames: [][]byte{{1, 2, 3, 4}}}
	if err := media.WriteVideo(in, v); err != nil {
		t.Fatal(err)
	}
	fp, _ := media.Fingerprint(in)
	task := mkTask(2, in, fp)
	cp := &Checkpoint{JobID: "job-1", InputFingerprint: fp,
		Params: Params{InputPath: in, DurationSec: 30, SegmentSec: 10, TargetKbps: 999, MinKbps: 100},
		Segments: []*model.Segment{{Index: 0, Status: model.StatusQueued}, {Index: 1, Status: model.StatusQueued}}}
	if _, err := Recover(cp, task, in); !errors.Is(err, ErrUnrecoverable) {
		t.Fatalf("want ErrUnrecoverable, got %v", err)
	}
}

func TestPathTraversalRejected(t *testing.T) {
	if _, _, err := NewStore(t.TempDir(), "../escape.json"); !errors.Is(err, safeio.ErrPathRejected) {
		t.Fatalf("want path rejection, got %v", err)
	}
}
