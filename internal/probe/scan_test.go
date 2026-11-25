package probe_test

import (
	"path/filepath"
	"testing"

	"mediaflow/internal/fixture"
	"mediaflow/internal/probe"
)

func writeSpec(t *testing.T, name string, s fixture.Spec) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := fixture.WriteAsset(path, s); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScanValidTracksAndStats(t *testing.T) {
	s := fixture.GoodSpec("valid")
	s.VideoSamples = 60
	s.KeyframeEvery = 30
	path := writeSpec(t, "good.mp4", s)
	r, err := probe.ScanFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if r.HasRejection() {
		t.Fatalf("unexpected rejection intervals: %+v", r.Intervals)
	}
	if len(r.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(r.Tracks))
	}
	if r.LibraryBoxCount == 0 {
		t.Fatal("go-mp4 box count should be populated")
	}
	if r.Stats.SampleCount != 60 || r.Stats.KeyframeCount < 2 {
		t.Fatalf("bad stats: %+v", r.Stats)
	}
	if r.Stats.DurationMS < 1900 || r.Stats.DurationMS > 2100 {
		t.Fatalf("duration ms = %d", r.Stats.DurationMS)
	}
}

func TestCorruptLengthAndTruncatedBoxes(t *testing.T) {
	s := fixture.GoodSpec("bad")
	s.VideoSamples = 12
	s.IncludeAudio = false

	lengthPath := writeSpec(t, "length.mp4", s)
	if err := fixture.CorruptBoxLength(lengthPath); err != nil {
		t.Fatal(err)
	}
	r, err := probe.ScanFile(lengthPath)
	if err != nil {
		t.Fatal(err)
	}
	if !r.HasRejection() || len(r.Intervals) == 0 {
		t.Fatalf("expected box length rejection: %+v", r)
	}
	var foundLength bool
	for _, in := range r.Intervals {
		if in.Class == "box_length_overflow" && in.ByteStart == 0 && in.Reject {
			foundLength = true
		}
	}
	if !foundLength {
		t.Fatalf("missing located length interval: %+v", r.Intervals)
	}

	truncPath := writeSpec(t, "trunc.mp4", s)
	if err := fixture.TruncateBox(truncPath); err != nil {
		t.Fatal(err)
	}
	rt, err := probe.ScanFile(truncPath)
	if err != nil {
		t.Fatal(err)
	}
	var foundTrunc bool
	for _, in := range rt.Intervals {
		if in.Class == "truncated_box" && in.ByteStart >= 0 && in.Quarantine {
			foundTrunc = true
		}
	}
	if !foundTrunc {
		t.Fatalf("missing truncation interval: %+v", rt.Intervals)
	}
}

func TestEditOOBIsMappedToTrackTime(t *testing.T) {
	s := fixture.GoodSpec("edit")
	s.VideoSamples = 30
	s.OOBEdit = true
	path := writeSpec(t, "edit.mp4", s)
	r, err := probe.ScanFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, in := range r.Intervals {
		if in.Class == "edit_list_out_of_bounds" && in.TrackID == 1 && in.Reject {
			found = true
			if in.StartMS <= 0 || in.EndMS <= in.StartMS {
				t.Fatalf("edit interval not mapped to time: %+v", in)
			}
		}
	}
	if !found {
		t.Fatalf("missing OOB edit interval: %+v", r.Intervals)
	}
}

func TestNonzeroStartAndSparseKeyframes(t *testing.T) {
	s := fixture.GoodSpec("boundary")
	s.VideoSamples = 150
	s.CompositionOffset = 15000
	s.KeyframeEvery = 150
	path := writeSpec(t, "boundary.mp4", s)
	r, err := probe.ScanFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var nonzero, sparse bool
	for _, in := range r.Intervals {
		switch in.Class {
		case "non_zero_start":
			nonzero = true
			if in.TrackID != 1 || in.StartMS != 500 {
				t.Fatalf("bad nonzero interval: %+v", in)
			}
		case "sparse_keyframes":
			sparse = true
			if in.StartSample != 0 || in.EndSample != 150 || in.StartMS != 0 {
				t.Fatalf("bad sparse interval: %+v", in)
			}
		}
	}
	if !nonzero || !sparse {
		t.Fatalf("nonzero=%v sparse=%v intervals=%+v", nonzero, sparse, r.Intervals)
	}
}

func TestExpectationMissingTrackAndTimebase(t *testing.T) {
	s := fixture.GoodSpec("one-track")
	s.VideoSamples = 12
	s.IncludeAudio = false
	path := writeSpec(t, "one.mp4", s)
	r, err := probe.ScanFile(path)
	if err != nil {
		t.Fatal(err)
	}
	item := probe.Item{ID: "x", RequiredTracks: []probe.TrackExpectation{
		{ID: 2, Type: "audio", Timescale: 48000},
		{ID: 1, Type: "video", Timescale: 999},
	}}
	intervals := probe.ValidateExpectations(item, r)
	if len(intervals) != 2 {
		t.Fatalf("expected missing track and timebase faults, got %+v", intervals)
	}
}
