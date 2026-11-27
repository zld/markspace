package keyframe

import (
	"testing"

	mp4scan "continuity/internal/mp4"
	"continuity/internal/timeline"
)

func track() *mp4scan.Track {
	t := &mp4scan.Track{ID: 2, Kind: "video", Clock: timeline.Clock{Timescale: 90000}}
	var start timeline.Ticks
	for i := 0; i < 6; i++ {
		t.Samples = append(t.Samples, mp4scan.Sample{
			Index: i, Start: start, Duration: 3000,
			Size: 100, Key: i == 0 || i == 4,
		})
		start += 3000
	}
	return t
}

func TestIntervals(t *testing.T) {
	ivs := Intervals(track())
	if len(ivs) != 2 {
		t.Fatalf("got %d intervals", len(ivs))
	}
	if ivs[0].Length() != 12000 || ivs[0].StartSample != 0 || ivs[0].EndSample != 4 {
		t.Fatalf("interval 0: %+v", ivs[0])
	}
	if ivs[1].Length() != 6000 || ivs[1].End != 18000 {
		t.Fatalf("interval 1: %+v", ivs[1])
	}
}

func TestFlagLong(t *testing.T) {
	rs := FlagLong(track(), 10000)
	if len(rs) != 1 {
		t.Fatalf("got %d ranges", len(rs))
	}
	r := rs[0]
	if r.Reason != "keyframe interval too long" || r.StartOffset != 0 || r.EndOffset != 3 {
		t.Fatalf("range: %+v", r)
	}
	if r.Start != 0 || r.End != 12000 {
		t.Fatalf("range ticks: %+v", r)
	}
	if rs2 := FlagLong(track(), 20000); len(rs2) != 0 {
		t.Fatalf("unexpected ranges: %+v", rs2)
	}
}
