package video

import (
	"testing"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/model"
)

func TestResolvePrefersContainerAndRecordsConflicts(t *testing.T) {
	res, errs, err := Resolve(model.TrackVideoMain, []model.ColorDeclaration{
		{Source: model.ColorFromBitstream, Primaries: 9, Transfer: 14, Matrix: 9},
		{Source: model.ColorFromContainer, Primaries: 1, Transfer: 1, Matrix: 1},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Selected.Primaries != 1 || res.Selected.Transfer != 1 || res.Selected.Matrix != 1 {
		t.Fatalf("selected = %+v, want BT.709", res.Selected)
	}
	if !res.Complete {
		t.Fatal("a complete declaration should mark the resolution complete")
	}
	if len(res.Conflicts) != 3 {
		t.Fatalf("conflicts = %d, want 3 (one per field): %+v", len(res.Conflicts), res.Conflicts)
	}
	if len(errs) != 3 {
		t.Fatalf("errs = %d, want 3 recoverable conflicts", len(errs))
	}
	for _, e := range errs {
		if !e.Recoverable || e.Stage != model.StageVideo {
			t.Fatalf("conflict must be a recoverable video error: %v", e)
		}
	}
	// Every source declaration is retained for the report.
	if len(res.Declarations) != 2 {
		t.Fatalf("declarations = %d, want both retained", len(res.Declarations))
	}
}

func TestResolveUnspecifiedNeverWins(t *testing.T) {
	res, _, err := Resolve(model.TrackVideoMain, []model.ColorDeclaration{
		{Source: model.ColorFromContainer, Primaries: 0},
		{Source: model.ColorFromBitstream, Primaries: 9, Transfer: 14, Matrix: 9},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Selected.Primaries != 9 || res.Selected.Transfer != 14 {
		t.Fatalf("selected = %+v, want bitstream values where the container is unspecified", res.Selected)
	}
}

func TestResolveEmptyIsUnrecoverable(t *testing.T) {
	_, _, err := Resolve(model.TrackVideoMain, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	pe, ok := model.AsError(err)
	if !ok || pe.Code != model.CodeColorUnknown || pe.Recoverable {
		t.Fatalf("unexpected error %v", err)
	}
}

func sampleAt(t *testing.T, ticks int64, scale uint32, sync bool) model.Sample {
	t.Helper()
	r, err := clock.FromTicks(ticks, scale)
	if err != nil {
		t.Fatal(err)
	}
	return model.Sample{Sync: sync, Time: model.TimeStamp{RepairedTime: clock.ToModel(r)}}
}

func TestValidateKeyframes(t *testing.T) {
	samples := []model.Sample{
		sampleAt(t, 0, 90000, true),
		sampleAt(t, 3000, 90000, false),
		sampleAt(t, 6000, 90000, true),
		sampleAt(t, 9000, 90000, true),
	}
	check, errs, err := ValidateKeyframes(model.TrackVideoMain, samples, []int{0, 2, 3}, 90000)
	if err != nil {
		t.Fatalf("ValidateKeyframes: %v", err)
	}
	if len(check.Index) != 3 {
		t.Fatalf("keyframes = %d, want 3", len(check.Index))
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	// Duplicates are ignored, non-sync candidates are rejected but recoverable.
	check, errs, err = ValidateKeyframes(model.TrackVideoMain, samples, []int{0, 0, 1}, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if len(check.Index) != 1 || check.OffBoundary != 1 || len(errs) != 1 {
		t.Fatalf("check = %+v errs=%d", check, len(errs))
	}
}

func TestValidateKeyframesOutOfRangeFatal(t *testing.T) {
	samples := []model.Sample{sampleAt(t, 0, 90000, true)}
	_, _, err := ValidateKeyframes(model.TrackVideoMain, samples, []int{5}, 90000)
	if err == nil {
		t.Fatal("expected an out-of-range error")
	}
	pe, ok := model.AsError(err)
	if !ok || pe.Recoverable {
		t.Fatalf("out-of-range index must be unrecoverable: %v", err)
	}
}

func TestValidateKeyframesNonMonotonicFatal(t *testing.T) {
	samples := []model.Sample{
		sampleAt(t, 6000, 90000, true),
		sampleAt(t, 3000, 90000, true),
	}
	_, _, err := ValidateKeyframes(model.TrackVideoMain, samples, []int{0, 1}, 90000)
	if err == nil {
		t.Fatal("expected a non-monotonic keyframe error")
	}
}
