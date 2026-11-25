package selfcheck

import (
	"reflect"

	"github.com/zld/mediacore/internal/model"
	"github.com/zld/mediacore/internal/pipeline"
)

type expectedRef struct {
	track   int
	samples []int
}

type expectedSegment struct {
	start      model.NS
	end        model.NS
	transition bool
	refs       []expectedRef
}

func standardExpectedSegments() []expectedSegment {
	return []expectedSegment{
		{start: 0, end: 400_000_000, refs: []expectedRef{
			{track: 1, samples: []int{0, 1, 2, 3}},
			{track: 3, samples: []int{0, 1, 2, 3}},
		}},
		{start: 400_000_000, end: 600_000_000, transition: true, refs: []expectedRef{
			{track: 1, samples: []int{4, 5}},
			{track: 2, samples: []int{0, 1}},
			{track: 3, samples: []int{4, 5}},
		}},
		{start: 600_000_000, end: 1_000_000_000, refs: []expectedRef{
			{track: 2, samples: []int{2, 3, 4, 5}},
			{track: 3, samples: []int{6, 7, 8, 9}},
		}},
	}
}

func checkStandardMappings(res *pipeline.Result) []string {
	var problems []string
	expected := standardExpectedSegments()
	if len(res.Segments) != len(expected) {
		return []string{"standard mapping segment count mismatch"}
	}
	for i, want := range expected {
		got := res.Segments[i]
		if got.Start != want.start || got.End != want.end {
			problems = append(problems, "segment boundary mismatch")
		}
		if got.Transition != want.transition {
			problems = append(problems, "transition flag mismatch")
		}
		byTrack := map[int]int{}
		for j, ref := range got.Sources {
			byTrack[ref.TrackID] = j
		}
		if len(got.Sources) != len(want.refs) {
			problems = append(problems, "source reference count mismatch")
			continue
		}
		for _, wr := range want.refs {
			idx, ok := byTrack[wr.track]
			if !ok {
				problems = append(problems, "missing source track in mapping")
				continue
			}
			if !reflect.DeepEqual(got.Sources[idx].SampleIndexes, wr.samples) {
				problems = append(problems, "sample mapping mismatch for source track")
			}
			if got.Sources[idx].Timeline.Start < 0 || got.Sources[idx].ByteRange.Start >= got.Sources[idx].ByteRange.End {
				problems = append(problems, "source mapping location invalid")
			}
		}
	}

	if len(res.Silence) != 1 || res.Silence[0].Start != 300_000_000 || res.Silence[0].End != 500_000_000 {
		problems = append(problems, "silence interval not mapped to common time base")
	}
	foundOverlapDuplicate := false
	for _, group := range res.Duplicates {
		has1, has2 := false, false
		for _, key := range group.Keys {
			if key == "segment:1:track:1" {
				has1 = true
			}
			if key == "segment:1:track:2" {
				has2 = true
			}
		}
		if has1 && has2 {
			foundOverlapDuplicate = true
		}
	}
	if !foundOverlapDuplicate {
		problems = append(problems, "transition overlap duplicate relation not marked")
	}
	return problems
}
