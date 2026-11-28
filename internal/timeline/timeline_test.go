package timeline

import (
	"strings"
	"testing"

	"medialab/internal/model"
)

func frame(track model.TrackID, seq uint32, pts int64) model.Frame {
	return model.Frame{
		Track: track, Sequence: seq, PTS: model.Timestamp(pts),
		Width: 64, Height: 48, Size: 8,
		Path: "x.bin", SHA256: strings.Repeat("a", 64),
		Color: model.Color{Primaries: "bt709", Transfer: "bt709", Range: "full"},
	}
}

func TestMergeStableOrdering(t *testing.T) {
	frames := []model.Frame{
		frame(2, 1, 100),
		frame(1, 2, 100),
		frame(1, 1, 100),
		frame(3, 1, 50),
	}
	tl, warns, err := Merge(frames, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Fatalf("warnings = %v", warns)
	}
	want := [][2]int{{3, 1}, {1, 1}, {1, 2}, {2, 1}}
	for i, w := range want {
		e := tl.Events[i]
		if int(e.Frame.Track) != w[0] || int(e.Frame.Sequence) != w[1] {
			t.Fatalf("event %d = %d/%d want %v", i, e.Frame.Track, e.Frame.Sequence, w)
		}
		if e.Index != i {
			t.Fatalf("event %d index = %d", i, e.Index)
		}
	}
	if tl.StartNS != 50 || tl.EndNS != 100 || tl.Span() != 50 {
		t.Fatalf("span = %d..%d", tl.StartNS, tl.EndNS)
	}
	if len(tl.Tracks) != 3 || tl.Tracks[0] != 1 || tl.Tracks[2] != 3 {
		t.Fatalf("tracks = %v", tl.Tracks)
	}
}

func TestMergeTrackIndex(t *testing.T) {
	frames := []model.Frame{
		frame(1, 1, 0), frame(1, 2, 10), frame(1, 3, 20),
		frame(2, 1, 5),
	}
	tl, _, err := Merge(frames, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if tl.Events[2].Frame.Track != 1 || tl.Events[2].TrackIndex != 1 {
		t.Fatalf("event = %+v", tl.Events[2])
	}
}

func TestMergeRejectsNegativeTime(t *testing.T) {
	frames := []model.Frame{frame(1, 1, -1)}
	_, _, err := Merge(frames, MergeOptions{})
	f, ok := model.AsFault(err)
	if !ok || f.Code != model.CodeTimeInvalid {
		t.Fatalf("err = %v", err)
	}
	if f.Track != 1 || f.Frame != 1 {
		t.Fatalf("location = %d/%d", f.Track, f.Frame)
	}
}

func TestMergeReportsRegression(t *testing.T) {
	frames := []model.Frame{
		frame(1, 1, 100),
		frame(1, 2, 40),
		frame(1, 3, 160),
	}
	tl, warns, err := Merge(frames, MergeOptions{})
	if err != nil {
		t.Fatalf("regression must be recoverable: %v", err)
	}
	if len(warns) != 1 || warns[0].Code != model.CodeTimeRegression || warns[0].Track != 1 {
		t.Fatalf("warnings = %v", warns)
	}
	// The returned warnings and the timeline's copy must agree, so callers that
	// merge both into one report do not double count.
	if len(tl.Warnings) != len(warns) {
		t.Fatalf("timeline warnings %d != returned %d", len(tl.Warnings), len(warns))
	}
	// Sorted output must still be monotone.
	for i := 1; i < len(tl.Events); i++ {
		if tl.Events[i].Frame.PTS < tl.Events[i-1].Frame.PTS {
			t.Fatalf("output not sorted at %d", i)
		}
	}

	_, _, err = Merge(frames, MergeOptions{RequireMonotonicPerTrack: true})
	if f, _ := model.AsFault(err); f == nil || f.Code != model.CodeTimeRegression {
		t.Fatalf("strict mode err = %v", err)
	}
}

func TestMergeDedupeRegressionWarnings(t *testing.T) {
	// Two identical frames at the same position collapse to one warning per
	// (code, track, frame); a distinct frame keeps its own.
	frames := []model.Frame{
		frame(1, 1, 100),
		frame(1, 2, 40),
		frame(1, 2, 40),
	}
	_, warns, err := Merge(frames, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 {
		t.Fatalf("warnings = %v", warns)
	}
}

func TestApplyOffsetsEmpty(t *testing.T) {
	if _, err := (&Timeline{}).ApplyOffsets(map[model.TrackID]int64{1: 10}); err == nil {
		t.Fatal("empty timeline accepted")
	}
}

func TestMergeEmpty(t *testing.T) {
	if _, _, err := Merge(nil, MergeOptions{}); err == nil {
		t.Fatal("empty timeline accepted")
	}
}

func TestApplyOffsets(t *testing.T) {
	frames := []model.Frame{
		frame(1, 1, 0), frame(1, 2, 40), frame(2, 1, 10),
	}
	tl, _, err := Merge(frames, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	shifted, err := tl.ApplyOffsets(map[model.TrackID]int64{2: 100})
	if err != nil {
		t.Fatal(err)
	}
	last := shifted.Events[len(shifted.Events)-1]
	if last.Frame.Track != 2 || last.Frame.PTS != 110 {
		t.Fatalf("shifted last = %+v", last.Frame)
	}
	if tl.Events[0].Frame.PTS != 0 {
		t.Fatal("ApplyOffsets mutated the source timeline")
	}
	if _, err := tl.ApplyOffsets(map[model.TrackID]int64{1: -1}); err == nil {
		t.Fatal("negative offset accepted")
	}
}

func TestAlignCadence(t *testing.T) {
	frames := []model.Frame{
		frame(1, 1, 0), frame(1, 2, 40_000_000), frame(1, 3, 80_000_000),
		frame(2, 1, 0), frame(2, 2, 40_000_000),
	}
	tl, _, err := Merge(frames, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Align(tl, AlignConfig{MaxErrorNS: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if rep.MaxErrorNS != 0 || len(rep.Violations) != 0 {
		t.Fatalf("report = %+v", rep)
	}

	// Perturb one frame by 5 ms: a 40 ms cadence tolerates less.
	bad := []model.Frame{
		frame(1, 1, 0), frame(1, 2, 40_000_000), frame(1, 3, 85_000_000),
	}
	tl2, _, err := Merge(bad, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rep2, err := Align(tl2, AlignConfig{MaxErrorNS: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if rep2.MaxErrorNS == 0 {
		t.Fatal("perturbation not detected")
	}
	if len(rep2.Violations) == 0 || rep2.Violations[0].Kind != model.KindIngest {
		t.Fatalf("violations = %v", rep2.Violations)
	}
}

func TestAlignEmpty(t *testing.T) {
	if _, err := Align(&Timeline{}, AlignConfig{}); err == nil {
		t.Fatal("empty alignment accepted")
	}
}

func TestMedianInterval(t *testing.T) {
	ev := []Event{
		{Frame: frame(1, 1, 0)},
		{Frame: frame(1, 2, 40)},
		{Frame: frame(1, 3, 80)},
		{Frame: frame(1, 4, 160)},
	}
	if got := medianInterval(ev); got != 40 {
		t.Fatalf("median = %d", got)
	}
}
