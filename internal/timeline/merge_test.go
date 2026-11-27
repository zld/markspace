package timeline

import (
	"testing"

	"media.rebuild/internal/model"
)

const ms = int64(1_000_000)

func TestMergeAlignsTracks(t *testing.T) {
	res, err := Merge(
		[]Event{
			{ID: "e0", Track: "client", Source: "events.json", TimeNs: 1_000 * ms, MediaTimeNs: 1_000 * ms},
			{ID: "e1", Track: "client", Source: "events.json", TimeNs: 2_000 * ms, MediaTimeNs: 2_000 * ms},
		},
		[]SampleRef{
			{ID: "m0", Track: "video", Source: "clip.mp4", TimeNs: 1_000 * ms, Label: "sample=0"},
			{ID: "m1", Track: "audio", Source: "clip.mp4", TimeNs: 1_005 * ms, Label: "sample=1"},
			{ID: "m2", Track: "video", Source: "clip.mp4", TimeNs: 2_090 * ms, Label: "sample=2"},
			{ID: "m3", Track: "video", Source: "clip.mp4", TimeNs: 3_500 * ms, Label: "sample=3"},
		},
		[]CueRef{
			{ID: "c0", Track: "subs", Source: "a.srt", Line: 1, TimeNs: 1_100 * ms, EndNs: 1_400 * ms},
			{ID: "c1", Track: "subs", Source: "a.srt", Line: 5, TimeNs: 2_200 * ms, EndNs: 2_500 * ms},
		},
		// The late video sample is 90 ms out, beyond the default 20 ms
		// grouping window, so widen the window to expose the skew.
		WithMediaTolerance(100*ms),
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.MaxSkewNs != 90*ms {
		t.Fatalf("max skew %d", res.MaxSkewNs)
	}
	if res.OverBudget != 1 {
		t.Fatalf("over budget %d, groups %+v", res.OverBudget, res.Groups)
	}
	if len(res.Anomalies) != 1 || res.Anomalies[0].MaxSkewNs != 90*ms {
		t.Fatalf("anomalies %+v", res.Anomalies)
	}
	// The over-threshold item keeps its original time; nothing was shifted.
	var moved *Item
	for i := range res.Items {
		if res.Items[i].ID == "m2" {
			moved = &res.Items[i]
		}
	}
	if moved == nil {
		t.Fatal("m2 missing")
	}
	if moved.TimeNs != 2_090*ms || moved.RefTimeNs != 2_000*ms || moved.SkewNs != 90*ms || !moved.Over {
		t.Fatalf("item m2 %+v", moved)
	}
	if len(res.Items) != 8 {
		t.Fatalf("items %d", len(res.Items))
	}
}

func TestCueJoinsContainingGroup(t *testing.T) {
	res, _ := Merge(
		[]Event{{ID: "e0", Track: "client", TimeNs: 0, MediaTimeNs: 5_000 * ms}},
		[]SampleRef{{ID: "m0", Track: "video", TimeNs: 5_000 * ms}},
		[]CueRef{{ID: "c0", Track: "subs", TimeNs: 4_900 * ms, EndNs: 5_600 * ms}},
		WithMediaTolerance(50*ms),
	)
	var cue *Item
	for i := range res.Items {
		if res.Items[i].ID == "c0" {
			cue = &res.Items[i]
		}
	}
	if cue == nil || cue.Group != "g0000" {
		t.Fatalf("cue %+v", cue)
	}
	// A caption that spans the event instant is covered, not skewed: its own
	// start is its reference.
	if cue.SkewNs != 0 || cue.Over {
		t.Fatalf("cue skew %+v", cue)
	}
}

func TestUngroupedItemsKeepOwnReference(t *testing.T) {
	res, _ := Merge(
		[]Event{{ID: "e0", Track: "client", TimeNs: 0}},
		[]SampleRef{{ID: "m0", Track: "video", TimeNs: 10_000 * ms}},
		nil,
	)
	for _, it := range res.Items {
		if it.Group == "unassigned" && it.SkewNs != 0 {
			t.Fatalf("unassigned item has skew: %+v", it)
		}
	}
	if res.MaxSkewNs != 0 || res.OverBudget != 0 {
		t.Fatalf("max %d over %d", res.MaxSkewNs, res.OverBudget)
	}
}

func TestExactlyAtThresholdIsInBudget(t *testing.T) {
	res, _ := Merge(
		[]Event{{ID: "e0", Track: "client", TimeNs: 0}},
		[]SampleRef{{ID: "m0", Track: "video", TimeNs: 40 * ms}},
		nil,
		WithMediaTolerance(40*ms),
	)
	if res.MaxSkewNs != 40*ms || res.OverBudget != 0 {
		t.Fatalf("max %d over %d", res.MaxSkewNs, res.OverBudget)
	}
}

func TestAnomalyRangesCoalesceAdjacent(t *testing.T) {
	events := []Event{
		{ID: "e0", TimeNs: 0},
		{ID: "e1", TimeNs: 1_000 * ms},
		{ID: "e2", TimeNs: 2_000 * ms},
		{ID: "e3", TimeNs: 3_000 * ms},
	}
	samples := []SampleRef{
		{ID: "m0", Track: "video", TimeNs: 100 * ms},
		{ID: "m1", Track: "video", TimeNs: 1_010 * ms},
		{ID: "m3", Track: "video", TimeNs: 3_050 * ms},
	}
	res, _ := Merge(events, samples, nil, WithMediaTolerance(200*ms))
	if res.OverBudget != 2 {
		t.Fatalf("over %d", res.OverBudget)
	}
	if len(res.Anomalies) != 2 {
		t.Fatalf("anomalies %+v", res.Anomalies)
	}
	if res.Anomalies[0].MaxSkewNs != 100*ms || res.Anomalies[1].MaxSkewNs != 50*ms {
		t.Fatalf("anomalies %+v", res.Anomalies)
	}
}

func TestSortingIsDeterministic(t *testing.T) {
	ev := []Event{{ID: "b", TimeNs: 5}, {ID: "a", TimeNs: 5}, {ID: "c", TimeNs: 1}}
	a, _ := Merge(ev, nil, nil)
	b, _ := Merge([]Event{ev[2], ev[0], ev[1]}, nil, nil)
	if len(a.Items) != len(b.Items) {
		t.Fatal("length differs")
	}
	for i := range a.Items {
		if a.Items[i] != b.Items[i] {
			t.Fatalf("item %d differs: %+v vs %+v", i, a.Items[i], b.Items[i])
		}
	}
}

func TestInvalidThreshold(t *testing.T) {
	if _, err := Merge(nil, nil, nil, WithThreshold(0)); !model.IsCode(err, model.CodeInvalidArgument) {
		t.Fatalf("error %v", err)
	}
}

func TestNegativeSkewAbsoluteValue(t *testing.T) {
	res, _ := Merge(
		[]Event{{ID: "e0", TimeNs: 100 * ms}},
		[]SampleRef{{ID: "m0", Track: "video", TimeNs: 20 * ms}},
		nil,
		WithMediaTolerance(100*ms),
	)
	var m *Item
	for i := range res.Items {
		if res.Items[i].ID == "m0" {
			m = &res.Items[i]
		}
	}
	if m.SkewNs != -80*ms || m.AbsSkewNs != 80*ms || !m.Over {
		t.Fatalf("item %+v", m)
	}
}

func TestAnomalyListsItemsAndTracks(t *testing.T) {
	res, _ := Merge(
		[]Event{
			{ID: "e0", Track: "client", TimeNs: 0},
			{ID: "e1", Track: "client", TimeNs: 1_000 * ms},
		},
		[]SampleRef{
			{ID: "m0", Track: "video", TimeNs: 100 * ms},   // 100 ms late
			{ID: "m1", Track: "audio", TimeNs: 1_050 * ms}, // 50 ms late
		},
		nil,
		WithMediaTolerance(200*ms),
	)
	if len(res.Anomalies) != 1 {
		t.Fatalf("anomalies %+v", res.Anomalies)
	}
	a := res.Anomalies[0]
	if len(a.Items) != 2 || a.Items[0] != "m0" || a.Items[1] != "m1" {
		t.Fatalf("items %v", a.Items)
	}
	if len(a.Tracks) != 2 || a.Tracks[0] != "audio" || a.Tracks[1] != "video" {
		t.Fatalf("tracks %v", a.Tracks)
	}
	// The two groups are contiguous (both over budget, no clean group
	// between) so they coalesce into one range covering both instants.
	if a.StartNs != 0 || a.EndNs != 1_000*ms {
		t.Fatalf("span [%d,%d]", a.StartNs, a.EndNs)
	}
}

func TestInBudgetItemNotListedInAnomaly(t *testing.T) {
	res, _ := Merge(
		[]Event{{ID: "e0", Track: "client", TimeNs: 0}},
		[]SampleRef{
			{ID: "bad", Track: "video", TimeNs: 100 * ms},
			{ID: "fine", Track: "audio", TimeNs: 10 * ms},
		},
		nil,
		WithMediaTolerance(200*ms),
	)
	if len(res.Anomalies) != 1 {
		t.Fatalf("anomalies %+v", res.Anomalies)
	}
	for _, id := range res.Anomalies[0].Items {
		if id == "fine" {
			t.Fatal("in-budget item listed as anomalous")
		}
	}
}
