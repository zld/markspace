package job

import (
	"errors"
	"testing"

	"replaycheck/internal/model"
)

func TestResumeAfterFailure(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	r := &Runner{State: NewState("normal", "src1", "ev1"), Resume: true}
	for _, stage := range []model.Stage{model.StageIngest, model.StageNormalize, model.StageSplit} {
		r.Begin(stage, "in-"+string(stage), false, "")
		r.Complete(stage, "out-"+string(stage))
	}
	r.Fail(model.StageDerive, 442404, model.NewError(model.StageDerive, "x", model.TrackRef{}, 442404, "boom"))
	if err := st.Save(r.State); err != nil {
		t.Fatal(err)
	}

	loaded, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	r2 := &Runner{State: loaded, Resume: true}
	if got := r2.ResumeAt("src1", "ev1"); got != model.StageDerive {
		t.Fatalf("resume at %s", got)
	}
	if loaded.LastOK != model.StageSplit || loaded.FailedAt != model.StageDerive {
		t.Fatalf("state lost recovery point: %+v", loaded)
	}
	if loaded.FailureOff != 442404 {
		t.Fatalf("offset=%d", loaded.FailureOff)
	}
}

func TestSourceChangeRestartsAtIngest(t *testing.T) {
	r := &Runner{State: NewState("normal", "src1", "ev1"), Resume: true}
	r.Begin(model.StageIngest, "x", false, "")
	r.Complete(model.StageIngest, "x")
	if got := r.ResumeAt("src2", "ev1"); got != model.StageIngest {
		t.Fatalf("changed source resumed at %s", got)
	}
}

func TestLoadMissingState(t *testing.T) {
	st := NewStore(t.TempDir())
	if _, err := st.Load(); !errors.Is(err, ErrNoState) {
		t.Fatalf("got %v", err)
	}
}

func TestStageTransitionsCountedOnce(t *testing.T) {
	r := &Runner{State: NewState("f", "s", "e")}
	r.Begin(model.StageIngest, "i", false, "")
	r.Begin(model.StageIngest, "i", true, "")
	if r.Transitions != 1 || r.Skips != 1 {
		t.Fatalf("transitions=%d skips=%d", r.Transitions, r.Skips)
	}
}
