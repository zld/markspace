package index

import (
	"testing"

	"rtx/internal/cmaf"
	"rtx/internal/model"
	"rtx/internal/repair"
)

func rate(t *testing.T) model.Rate {
	t.Helper()
	r, err := model.NewRateRatio(720000, 1)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func timeline(t *testing.T, frags []*cmaf.Fragment) *repair.Timeline {
	t.Helper()
	inputs := make([]repair.Input, len(frags))
	for i, f := range frags {
		inputs[i] = repair.Input{Source: "a", Order: i, Frag: f}
	}
	tl, err := repair.BuildTimeline(rate(t), repair.DefaultTrackSpecs(), map[string]int64{"a": 0}, inputs)
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

func fragment(t *testing.T, path string, seq uint32, tfd uint64, samples int, keyEvery int) *cmaf.Fragment {
	t.Helper()
	var video []cmaf.MuxSample
	for i := 0; i < samples; i++ {
		video = append(video, cmaf.MuxSample{
			Data:     make([]byte, 16+i),
			Duration: 3600,
			Key:      i%keyEvery == 0,
		})
	}
	raw, err := cmaf.BuildFragment(cmaf.FragmentSpec{
		Sequence: seq, VideoTFD: tfd, Video: video,
		Audio: []cmaf.MuxSample{{Data: make([]byte, 8), Duration: uint32(samples) * 1920}},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := cmaf.ParseBytes(path, raw)
	if err != nil {
		t.Fatal(err)
	}
	f, err := cmaf.ParseFragment(parsed)
	if err != nil {
		t.Fatal(err)
	}
	f.Source = "a"
	return f
}

func TestIndexSeekReturnsPrecedingKeyframe(t *testing.T) {
	tl := timeline(t, []*cmaf.Fragment{
		fragment(t, "f1.m4s", 1, 0, 6, 3),
		fragment(t, "f2.m4s", 2, 6*3600, 6, 3),
	})
	idx, err := Build(tl, TrackKeys{VideoTrackID: 1, AudioTrackID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.Validate(); err != nil {
		t.Fatal(err)
	}
	// Seek to 0.15s. The keyframes are at sample 0 (0s) and sample 3 (0.12s).
	r := idx.Rate()
	target := model.MustTime(int64(0.15*float64(r.Num/r.Den)), r)
	res, err := idx.Seek(1, target)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Key.IsKeyframe {
		t.Fatal("seek returned a non-keyframe")
	}
	if res.Key.TrackTicks != 3*3600 {
		t.Fatalf("keyframe track ticks = %d, want %d", res.Key.TrackTicks, 3*3600)
	}
	if res.SampleOffset != res.Key.ByteOffset || res.SampleOffset <= 0 {
		t.Fatalf("sample offset = %d", res.SampleOffset)
	}
	if res.DistanceMs < 0 || res.DistanceMs > 40 {
		t.Fatalf("distance = %.2fms", res.DistanceMs)
	}
}

func TestIndexSeekBeforeFirstKeyframeFails(t *testing.T) {
	tl := timeline(t, []*cmaf.Fragment{fragment(t, "f1.m4s", 1, 3600, 3, 3)})
	idx, err := Build(tl, TrackKeys{VideoTrackID: 1, AudioTrackID: 2})
	if err != nil {
		t.Fatal(err)
	}
	// First keyframe is at 3600 video ticks = 28800 media ticks.
	r := idx.Rate()
	target := model.MustTime(1000, r)
	_, err = idx.Seek(1, target)
	if err == nil {
		t.Fatal("expected no_keyframe error")
	}
	if e, _ := model.As(err); e == nil || e.Code != CodeNoKeyframe {
		t.Fatalf("error = %v", err)
	}
}

func TestIndexRecordsOriginalAndRepairedMapping(t *testing.T) {
	tl := timeline(t, []*cmaf.Fragment{
		fragment(t, "f1.m4s", 1, 0, 3, 3),
		// Repaired: tfdt rewinds to 1800 from a previous end of 3*3600.
		fragment(t, "f2.m4s", 2, 1800, 3, 3),
	})
	idx, err := Build(tl, TrackKeys{VideoTrackID: 1, AudioTrackID: 2})
	if err != nil {
		t.Fatal(err)
	}
	var repaired *Entry
	for i := range idx.Entries {
		e := &idx.Entries[i]
		if e.Fragment == "f2.m4s" && e.TrackID == 1 {
			repaired = e
			break
		}
	}
	if repaired == nil {
		t.Fatal("missing second-fragment entry")
	}
	if repaired.MediaTicks == repaired.OriginalMediaTicks {
		t.Fatalf("original and repaired mapping are identical: %+v", repaired)
	}
	// Original tfdt 1800 at 90k -> 14400 media ticks; repaired 10800 -> 86400.
	if repaired.OriginalMediaTicks != 14400 {
		t.Fatalf("original = %d, want 14400", repaired.OriginalMediaTicks)
	}
	if repaired.MediaTicks != 86400 {
		t.Fatalf("repaired = %d, want 86400", repaired.MediaTicks)
	}
}

func TestIndexValidateRejectsInverted(t *testing.T) {
	idx := &Index{MediaNum: 720000, MediaDen: 1, Entries: []Entry{
		{TrackID: 1, MediaTicks: 100, IsKeyframe: true, Size: 1},
		{TrackID: 1, MediaTicks: 50, Size: 1},
	}}
	if err := idx.Validate(); err == nil {
		t.Fatal("expected non-monotonic error")
	}
}

func TestIndexValidateRejectsMissingKeyframe(t *testing.T) {
	idx := &Index{MediaNum: 720000, MediaDen: 1, Entries: []Entry{
		{TrackID: 1, MediaTicks: 100, Size: 1},
	}}
	if err := idx.Validate(); err == nil {
		t.Fatal("expected missing keyframe error")
	}
}

func TestIndexDeterministic(t *testing.T) {
	mk := func() *Index {
		tl := timeline(t, []*cmaf.Fragment{
			fragment(t, "f1.m4s", 1, 0, 4, 2),
			fragment(t, "f2.m4s", 2, 4*3600, 4, 2),
		})
		idx, err := Build(tl, TrackKeys{VideoTrackID: 1, AudioTrackID: 2})
		if err != nil {
			t.Fatal(err)
		}
		return idx
	}
	a, b := mk(), mk()
	if len(a.Entries) != len(b.Entries) {
		t.Fatalf("entry counts differ: %d vs %d", len(a.Entries), len(b.Entries))
	}
	for i := range a.Entries {
		if a.Entries[i] != b.Entries[i] {
			t.Fatalf("entry %d differs:\n%+v\n%+v", i, a.Entries[i], b.Entries[i])
		}
	}
}

func TestIndexEntriesAt(t *testing.T) {
	tl := timeline(t, []*cmaf.Fragment{fragment(t, "f1.m4s", 1, 0, 6, 1)})
	idx, err := Build(tl, TrackKeys{VideoTrackID: 1, AudioTrackID: 2})
	if err != nil {
		t.Fatal(err)
	}
	r := idx.Rate()
	start := model.MustTime(0, r)
	end := model.MustTime(2*28800, r) // two 3600-tick video samples
	got, err := idx.EntriesAt(1, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("entries = %d, want 2", len(got))
	}
}
