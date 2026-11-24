package timeline

import (
	"testing"

	"mediaaudit/internal/model"
)

func vidTrack() model.Track { return model.Track{ID: "v1", Kind: model.KindVideo} }
func subTrack() model.Track { return model.Track{ID: "s1", Kind: model.KindSubtitle} }

func videoAt(starts ...int64) []Sample {
	out := make([]Sample, len(starts))
	for i, s := range starts {
		out[i] = Sample{Track: vidTrack(), Index: i, Start: model.RatFromInt(s),
			Duration: model.RatFromInt(1), Keyframe: i%2 == 0, Origin: "index",
			Label: "f" + itoa(i)}
	}
	return out
}

func BaseBuild() BuildOptions {
	return BuildOptions{
		VideoSamples: videoAt(0, 1, 2, 3, 4),
		Subtitles:    []model.Track{subTrack()},
		Cues: map[string][]Cue{
			"s1": {{Track: subTrack(), Index: 1, Start: model.RatFromInt(0), End: model.RatFromInt(2), Text: "hello"}},
		},
	}
}

func TestBuildOrdersDeterministically(t *testing.T) {
	in := BaseBuild()
	a := Build(in)
	b := Build(in)
	if len(a.Items) != len(b.Items) {
		t.Fatal("item count differs")
	}
	for i := range a.Items {
		if a.Items[i].Key != b.Items[i].Key || a.Items[i].Seq != i {
			t.Fatalf("item %d differs: %q vs %q", i, a.Items[i].Key, b.Items[i].Key)
		}
	}
	// Video at 0,1,2,3,4 and subtitle 0..2 split into 0-1,1-2: total 7.
	if len(a.Items) != 7 {
		t.Fatalf("items=%d want 7", len(a.Items))
	}
	if a.Items[0].Kind != ItemVideo || a.Items[1].Kind != ItemSubtitle {
		t.Fatalf("kind priority wrong at 0: %v %v", a.Items[0].Kind, a.Items[1].Kind)
	}
}

func TestBuildSplitsAtVideoCuts(t *testing.T) {
	in := BaseBuild()
	in.Cues["s1"] = []Cue{{Track: subTrack(), Index: 1, Start: model.RatFromInt(0), End: model.RatFromInt(3), Text: "x"}}
	res := Build(in)
	subs := 0
	for _, it := range res.Items {
		if it.Kind == ItemSubtitle {
			subs++
		}
	}
	if subs != 3 {
		t.Fatalf("subtitle pieces=%d want 3", subs)
	}
}

func TestBuildSnapsToCut(t *testing.T) {
	in := BaseBuild()
	in.Cues["s1"] = []Cue{{Track: subTrack(), Index: 1, Start: model.MustRat(105, 100), End: model.MustRat(295, 100), Text: "x"}}
	in.Opts.Tolerance = model.MustRat(1, 10)
	res := Build(in)
	if res.Snapped != 2 {
		t.Fatalf("snapped=%d want 2", res.Snapped)
	}
	// Snapped span 1..3 is cut at video sample starts 1 and 2, so the pieces
	// are 1..2 and 2..3; together they tile the snapped boundary exactly.
	var starts, ends []model.Rat
	for _, it := range res.Items {
		if it.Kind == ItemSubtitle {
			starts = append(starts, it.Span.Start)
			ends = append(ends, it.Span.End)
		}
	}
	if len(starts) != 2 || !starts[0].Equal(model.RatFromInt(1)) || !ends[len(ends)-1].Equal(model.RatFromInt(3)) {
		t.Fatalf("snapped pieces %v..%v", starts, ends)
	}
}

func TestBuildNoSnapWhenTooFar(t *testing.T) {
	in := BaseBuild()
	in.Cues["s1"] = []Cue{{Track: subTrack(), Index: 1, Start: model.MustRat(150, 100), End: model.MustRat(250, 100), Text: "x"}}
	in.Opts.Tolerance = model.MustRat(1, 10)
	res := Build(in)
	if res.Snapped != 0 {
		t.Fatalf("snapped=%d want 0", res.Snapped)
	}
}

func TestBuildSnapPrefersEarlierOnTie(t *testing.T) {
	// Cue boundary exactly between cuts at 2 and 3 with tolerance 1 s.
	in := BuildOptions{
		VideoSamples: videoAt(0, 2, 3, 4),
		Subtitles:    []model.Track{subTrack()},
		Cues: map[string][]Cue{"s1": {{Track: subTrack(), Index: 1,
			Start: model.MustRat(5, 2), End: model.RatFromInt(4), Text: "t"}}},
		Opts: Options{Tolerance: model.RatFromInt(1)},
	}
	res := Build(in)
	// 2.5 is 0.5 from both 2 and 3, so the earlier cut must win.
	var first model.Rat
	for _, it := range res.Items {
		if it.Kind == ItemSubtitle {
			first = it.Span.Start
			break
		}
	}
	if !first.Equal(model.RatFromInt(2)) {
		t.Fatalf("tie snapped to %s want 2", first)
	}
}

func TestBuildCollapsedCueReported(t *testing.T) {
	in := BuildOptions{
		VideoSamples: videoAt(0, 1),
		Subtitles:    []model.Track{subTrack()},
		Cues: map[string][]Cue{"s1": {{Track: subTrack(), Index: 1,
			Start: model.MustRat(99, 100), End: model.MustRat(101, 100), Text: "tiny"}}},
		Opts: Options{Tolerance: model.MustRat(1, 2)},
	}
	res := Build(in)
	found := false
	for _, e := range res.Errors {
		if e.Code == model.CodeTimelineAlign {
			found = true
		}
	}
	if !found {
		t.Fatalf("collapsed cue not reported: %v", res.Errors)
	}
}

func TestBuildGapCheck(t *testing.T) {
	samples := videoAt(0, 1, 10)
	in := BuildOptions{VideoSamples: samples, Opts: Options{MaxGap: model.RatFromInt(2)}}
	res := Build(in)
	if len(res.Errors) != 1 || res.Errors[0].Code != model.CodeTimelineAlign {
		t.Fatalf("gap errors %v", res.Errors)
	}
}

func TestBuildTimecodeMarks(t *testing.T) {
	in := BaseBuild()
	in.Timecodes = []TimecodeMark{{Track: model.Track{ID: "tc1", Kind: model.KindTimecode},
		Index: 0, Label: "00:00:01:00", Time: model.RatFromInt(1), Segment: "seg0"}}
	res := Build(in)
	var mark *Item
	for i := range res.Items {
		if res.Items[i].Kind == ItemTimecode {
			mark = &res.Items[i]
		}
	}
	if mark == nil || mark.Payload != "00:00:01:00" || !mark.Span.Start.Equal(model.RatFromInt(1)) {
		t.Fatalf("timecode item %+v", mark)
	}
}

func TestBuildBoundariesSorted(t *testing.T) {
	res := Build(BaseBuild())
	for i := 1; i < len(res.Boundaries); i++ {
		if res.Boundaries[i].Time.Cmp(res.Boundaries[i-1].Time) < 0 {
			t.Fatalf("boundaries unsorted at %d", i)
		}
	}
}

func TestBuildRejectsNegativeDuration(t *testing.T) {
	in := BaseBuild()
	in.VideoSamples[2].Duration = model.RatFromInt(-1)
	res := Build(in)
	found := false
	for _, e := range res.Errors {
		if e.Code == model.CodeTimelineAlign {
			found = true
		}
	}
	if !found {
		t.Fatal("negative sample duration must be reported")
	}
	for _, it := range res.Items {
		if it.Kind == ItemVideo && it.Span.End.Cmp(it.Span.Start) < 0 {
			t.Fatalf("reversed video span committed: %+v", it.Span)
		}
	}
}

func TestBuildKeepsZeroDurationSample(t *testing.T) {
	in := BaseBuild()
	in.VideoSamples[2].Duration = model.RatFromInt(0)
	res := Build(in)
	for _, e := range res.Errors {
		if e.Code == model.CodeTimelineAlign {
			t.Fatalf("zero duration must be accepted: %v", e)
		}
	}
}

func TestSnapAtExactDistanceBoundary(t *testing.T) {
	// A boundary exactly tol away from a cut may snap; cut points on both
	// sides at equal distance resolve to the earlier one.
	in := BaseBuild()
	in.VideoSamples = videoAt(0, 2, 4)
	in.Cues["s1"] = []Cue{{Track: subTrack(), Index: 1, Start: model.RatFromInt(1), End: model.RatFromInt(4), Text: "x"}}
	in.Opts.Tolerance = model.RatFromInt(1)
	res := Build(in)
	if res.Snapped == 0 {
		t.Fatal("a boundary exactly tol away should snap")
	}
	// The cue start at 1 is equidistant from cuts 0 and 2; the earlier must
	// win, which keeps the result stable.
	var start model.Rat
	for _, it := range res.Items {
		if it.Kind == ItemSubtitle {
			start = it.Span.Start
			break
		}
	}
	if !start.Equal(model.RatFromInt(0)) {
		t.Fatalf("equidistant snap chose %s, want 0", start)
	}
}
