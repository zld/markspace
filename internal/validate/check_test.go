package validate

import (
	"sort"
	"testing"

	"mediasync/internal/clock"
	"mediasync/internal/issue"
	"mediasync/internal/stream"
	"mediasync/internal/subtitle"
	"mediasync/internal/timeline"
)

func video() *stream.MP4Result {
	return &stream.MP4Result{
		Input: "v.mp4",
		Track: stream.TrackInfo{Timescale: 24000, Duration: clock.FromMilliseconds(10000), Handler: "vide"},
		Keyframes: []stream.Keyframe{
			{Index: 1, DTS: 0, PTS: 0, Offset: 40, Size: 100},
			{Index: 25, DTS: clock.FromMilliseconds(1000), PTS: clock.FromMilliseconds(1000), Offset: 290000, Size: 100},
			{Index: 49, DTS: clock.FromMilliseconds(2000), PTS: clock.FromMilliseconds(2000), Offset: 580000, Size: 100},
		},
		MinGap: clock.FromMilliseconds(1000), MedianGap: clock.FromMilliseconds(1000), MaxGap: clock.FromMilliseconds(1000),
		Intervals: []stream.Interval{{From: 1, To: 25, DTSGap: clock.FromMilliseconds(1000)}},
	}
}

func srtTrack() subtitle.Track {
	return subtitle.Track{Input: "s.srt", Kind: "srt", Cues: []subtitle.Cue{
		{Index: 1, Start: clock.FromMilliseconds(1000), End: clock.FromMilliseconds(1800), StartLn: 1, EndLn: 2},
		{Index: 2, Start: clock.FromMilliseconds(2000), End: clock.FromMilliseconds(2600), StartLn: 4, EndLn: 5},
	}}
}

func TestCueMappingExactTick(t *testing.T) {
	res := Analyze(Spec{Name: "ok", Window: clock.FromMilliseconds(2000), Cues: []subtitle.Track{srtTrack()}, MP4: video()})
	if len(res.Issues) != 0 {
		t.Fatalf("issues: %v", res.Issues)
	}
	if len(res.CueMaps) != 2 {
		t.Fatalf("maps=%d", len(res.CueMaps))
	}
	for _, m := range res.CueMaps {
		if m.TimeDev > 1 {
			t.Fatalf("cue %d dev=%s > 1 tick", m.CueIndex, m.TimeDev)
		}
		if m.Keyframe == 0 {
			t.Fatalf("cue %d not mapped", m.CueIndex)
		}
	}
	if res.MaxTimeDev > 1 {
		t.Fatalf("max dev %s", res.MaxTimeDev)
	}
}

func TestCueOutOfRange(t *testing.T) {
	tr := subtitle.Track{Input: "s.srt", Cues: []subtitle.Cue{
		{Index: 1, Start: clock.FromMilliseconds(9000), End: clock.FromMilliseconds(11000), StartLn: 1, EndLn: 2},
	}}
	res := Analyze(Spec{Name: "oor", Window: clock.FromMilliseconds(2000), Cues: []subtitle.Track{tr}, MP4: video()})
	found := false
	for _, it := range res.Issues {
		if it.Category == issue.CueOutOfRange && it.Loc.Line == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no out-of-range issue: %v", res.Issues)
	}
}

func TestDriftExceededKeepsOriginal(t *testing.T) {
	// Cues diverge 300 ms per second against media: far beyond 100 ms/10 min.
	var cues []subtitle.Cue
	for i := 0; i < 5; i++ {
		at := clock.FromMilliseconds(int64(i) * 1000)
		cues = append(cues, subtitle.Cue{
			Index: i + 1, Start: at, End: at + clock.FromMilliseconds(500),
			StartLn: i*2 + 1, EndLn: i*2 + 2,
		})
	}
	tr := subtitle.Track{Input: "s.srt", Cues: cues}
	res := Analyze(Spec{Name: "drift", Window: clock.FromMilliseconds(2000), Cues: []subtitle.Track{tr}, MP4: video()})
	if !res.DriftKnown || !res.Drift.Exceeded {
		t.Fatalf("drift not flagged: %+v", res.Drift)
	}
	if res.Drift.Applied != 0 {
		t.Fatalf("applied correction on exceeded drift: %s", res.Drift.Applied)
	}
	found := false
	for _, it := range res.Issues {
		if it.Category == issue.DriftExceeded {
			found = true
		}
	}
	if !found {
		t.Fatal("no drift issue")
	}
	// Original cue times are untouched.
	if tr.Cues[1].Start != clock.FromMilliseconds(1000) {
		t.Fatalf("cue time was rewritten: %s", tr.Cues[1].Start)
	}
}

func TestDamagedRangesMerged(t *testing.T) {
	v := video()
	v.Damaged = []issue.ByteRange{
		{Input: "v.mp4", Start: 100, End: 110, Categories: []issue.Category{issue.MP4Truncated}},
		{Input: "v.mp4", Start: 112, End: 120, Categories: []issue.Category{issue.RTPCorrupt}},
		{Input: "v.mp4", Start: 500, End: 510, Categories: []issue.Category{issue.MP4Truncated}},
	}
	v.Issues = append(v.Issues, issue.New(issue.MP4Truncated, "v.mp4", "x").WithByte(100))
	res := Analyze(Spec{Name: "dmg", Window: clock.FromMilliseconds(2000), MP4: v})
	if len(res.Damaged) != 2 {
		t.Fatalf("ranges=%v", res.Damaged)
	}
	if res.Damaged[0].Start != 100 || res.Damaged[0].End != 120 {
		t.Fatalf("first range=%v", res.Damaged[0])
	}
}

func TestZeroWindowRejectsLate(t *testing.T) {
	events := []stream.Keyframe{
		{Index: 1, DTS: 0, PTS: 0},
		{Index: 2, DTS: clock.FromMilliseconds(3000), PTS: clock.FromMilliseconds(3000)},
		{Index: 3, DTS: clock.FromMilliseconds(1000), PTS: clock.FromMilliseconds(1000)},
	}
	v := video()
	v.Keyframes = events
	// Admit in the given (non-canonical) order: 0, 3000, then the late 1000.
	arrival := func(evs []timeline.Event) []timeline.Event {
		order := map[string]int{"kf:1": 0, "kf:2": 1, "kf:3": 2}
		out := append([]timeline.Event(nil), evs...)
		sort.SliceStable(out, func(i, j int) bool { return order[out[i].Ref] < order[out[j].Ref] })
		return out
	}
	res := Analyze(Spec{Name: "zw", Window: 0, MP4: v, Arrival: arrival})
	if res.Unrecoverable != 1 {
		t.Fatalf("unrecoverable=%d", res.Unrecoverable)
	}
}

func TestArrivalReorderingDeterministic(t *testing.T) {
	spec := Spec{Name: "arr", Window: clock.FromMilliseconds(2000), Cues: []subtitle.Track{srtTrack()}, MP4: video()}
	base := Analyze(spec)
	rev := func(evs []timeline.Event) []timeline.Event {
		out := append([]timeline.Event(nil), evs...)
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
		return out
	}
	spec.Arrival = rev
	got := Analyze(spec)
	if len(base.Events) != len(got.Events) {
		t.Fatalf("event count %d vs %d", len(base.Events), len(got.Events))
	}
	for i := range base.Events {
		if base.Events[i].Ref != got.Events[i].Ref || base.Events[i].Time != got.Events[i].Time {
			t.Fatalf("canonical event %d differs: %+v vs %+v", i, base.Events[i], got.Events[i])
		}
	}
}
