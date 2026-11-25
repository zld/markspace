package index_test

import (
	"errors"
	"testing"

	"mediacore/internal/clock"
	"mediacore/internal/fixture"
	"mediacore/internal/index"
	"mediacore/internal/mp4"
)

func buildNormalIndex(t *testing.T) *index.Index {
	t.Helper()
	dir := t.TempDir()
	set, err := fixture.GenerateNormal(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := mp4.LoadManifest(set.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mp4.ParseManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	ps := parsed[0]
	idx, err := index.Build(ps.Source.ID, ps.File.Size(), ps.MP4)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestRangeQueryUsesPrecedingKeyframeAndHalfOpenBoundaries(t *testing.T) {
	idx := buildNormalIndex(t)
	if len(idx.Keyframes) != fixture.NormalSamples/fixture.KeyframeEvery {
		t.Fatalf("keyframes = %d", len(idx.Keyframes))
	}
	r := index.Range{Start: 600_000_000_000, End: 612_000_000_000}
	got, err := idx.Query(r)
	if err != nil {
		t.Fatal(err)
	}
	if got.Keyframe == nil || got.Keyframe.SampleNumber != 571 {
		t.Fatalf("covering keyframe = %+v, want 571", got.Keyframe)
	}
	if len(got.Samples) != 12 {
		t.Fatalf("samples = %d, want 12", len(got.Samples))
	}
	if got.Samples[0].Number != 599 || got.Samples[len(got.Samples)-1].Number != 610 {
		t.Fatalf("sample bounds = %d..%d", got.Samples[0].Number, got.Samples[len(got.Samples)-1].Number)
	}

	oneSecond := index.Range{Start: 600_000_000_000, End: 601_000_000_000}
	part, err := idx.Query(oneSecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(part.Samples) != 1 || part.Samples[0].Number != 599 {
		t.Fatalf("half-open second returned %+v", part.Samples)
	}

	key := idx.Keyframes[20]
	atKey, err := idx.Query(index.Range{Start: key.EventStart, End: key.EventStart + 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if atKey.Keyframe.SampleNumber != key.SampleNumber {
		t.Fatalf("exact key start chose %d, want %d", atKey.Keyframe.SampleNumber, key.SampleNumber)
	}
}

func TestMissingTrackAndOverlapAreLocated(t *testing.T) {
	_, err := index.QueryNamed(map[string]*index.Index{}, []string{"nope"},
		index.Range{Start: 0, End: clock.EventTime(timeSecond)})
	var ce *clock.Error
	if !errors.As(err, &ce) || ce.Category != clock.CategoryMissingTrack {
		t.Fatalf("missing track err = %#v", err)
	}
	err = index.ValidateDisjoint("intervals.txt", []index.LocatedInterval{
		{Start: 0, End: 10, Offset: 4},
		{Start: 9, End: 12, Offset: 99},
	})
	if !errors.As(err, &ce) || ce.Category != clock.CategoryOverlappingInterval || ce.Offset != 99 {
		t.Fatalf("overlap err = %#v", err)
	}
}

const timeSecond = 1_000_000_000
