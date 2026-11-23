package fanin

import (
	"testing"

	"mediad/internal/media"
)

// stream builds an ordered representation stream from per-track timestamps.
func stream(rep string, tracks map[media.TrackID][]int64) []media.Block {
	var out []media.Block
	for tid, times := range tracks {
		for _, t := range times {
			out = append(out, media.Block{Track: tid, TimeNs: t, Rep: rep})
		}
	}
	return SortStream(out)
}

func TestMergeCombinesTracksByTime(t *testing.T) {
	video := []int64{0, 40e6, 80e6}
	audio := []int64{0, 20e6, 40e6, 60e6, 80e6}
	streams := map[string][]media.Block{
		"360p":  stream("360p", map[media.TrackID][]int64{1: video, 2: audio}),
		"1080p": stream("1080p", map[media.TrackID][]int64{1: video, 2: audio}),
	}
	res, err := Merge(streams, []string{"360p", "1080p"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Blocks) != 8 {
		t.Fatalf("merged %d blocks want 8", len(res.Blocks))
	}
	for i := 1; i < len(res.Blocks); i++ {
		if res.Blocks[i-1].TimeNs > res.Blocks[i].TimeNs {
			t.Fatalf("merge not time ordered at %d", i)
		}
	}
	if len(res.Mismatches) != 0 {
		t.Fatalf("unexpected mismatches: %v", res.Mismatches)
	}
	if res.Blocks[0].Rep != "360p" {
		t.Fatalf("default representation %q, want first in ladder", res.Blocks[0].Rep)
	}
}

func TestDuplicateTimesCollapse(t *testing.T) {
	times := []int64{0, 40e6, 80e6}
	streams := map[string][]media.Block{
		"360p":  stream("360p", map[media.TrackID][]int64{1: times}),
		"720p":  stream("720p", map[media.TrackID][]int64{1: times}),
		"1080p": stream("1080p", map[media.TrackID][]int64{1: times}),
	}
	res, err := Merge(streams, []string{"360p", "720p", "1080p"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Blocks) != 3 {
		t.Fatalf("merged %d blocks, want 3 (duplicates collapsed)", len(res.Blocks))
	}
	for _, b := range res.Blocks {
		if b.Rep != "360p" {
			t.Fatalf("ladder order not honoured: kept %s", b.Rep)
		}
	}
}

func TestPlanSwitchesRepresentation(t *testing.T) {
	streams := map[string][]media.Block{
		"360p":  stream("360p", map[media.TrackID][]int64{1: {0, 40e6, 80e6}}),
		"1080p": stream("1080p", map[media.TrackID][]int64{1: {0, 40e6, 80e6}}),
	}
	res, err := Merge(streams, []string{"360p", "1080p"}, Options{
		Plan: []Switch{{Track: 1, AtNs: 40e6, ToRep: "1080p"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.SwitchCount != 1 || len(res.Boundaries) != 1 {
		t.Fatalf("switches = %d boundaries = %d", res.SwitchCount, len(res.Boundaries))
	}
	if res.Mismatches != nil {
		t.Fatalf("unexpected mismatches: %v", res.Mismatches)
	}
	// Blocks at and after the switch must come from 1080p.
	for _, b := range res.Blocks {
		want := "360p"
		if b.TimeNs >= 40e6 {
			want = "1080p"
		}
		if b.Rep != want {
			t.Fatalf("block at %d ms from %s, want %s", b.TimeNs/1e6, b.Rep, want)
		}
	}
	b := res.Boundaries[0]
	if b.Track != 1 || b.FromRep != "360p" || b.ToRep != "1080p" || b.TimeNs != 40e6 {
		t.Fatalf("boundary lacks context: %+v", b)
	}
}
