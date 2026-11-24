package maptrack_test

import (
	"testing"

	"mediapack/internal/clock"
	"mediapack/internal/demux"
	"mediapack/internal/fault"
	"mediapack/internal/maptrack"
)

func catalog() maptrack.Catalog {
	return maptrack.Catalog{
		MP4: []*demux.Document{{Path: "a.mp4", Tracks: []*demux.Track{
			{ID: 1, Timebase: clock.TickRate(24000)},
			{ID: 2, Timebase: clock.TickRate(48000)},
		}}},
		RTP: []*demux.RTPDocument{{Path: "a.rtp", Streams: []*demux.RTPStream{{
			Key: demux.RTPSourceKey{SSRC: 7, PayloadType: 111}, Timebase: clock.TickRate(48000),
		}}}},
	}
}

func TestApplyMappingOneToOneAndOrder(t *testing.T) {
	tb48 := clock.TickRate(48000)
	rules := []maptrack.Rule{
		{Input: "a.mp4", TrackID: 1, TargetID: 10},
		{Input: "a.mp4", TrackID: 2, TargetID: 20, RequireTB: &tb48},
		{Input: "a.rtp", RTP: &demux.RTPSourceKey{SSRC: 7, PayloadType: 111}, TargetID: 30},
	}
	table, err := maptrack.Apply(catalog(), rules)
	if err != nil {
		t.Fatal(err)
	}
	if got := []uint32{table.Tracks[0].TargetID, table.Tracks[1].TargetID, table.Tracks[2].TargetID}; got[0] != 10 || got[1] != 20 || got[2] != 30 {
		t.Fatalf("order=%v", got)
	}
	if !table.CommonTimebase.Equal(clock.TickRate(48000)) {
		t.Fatalf("common=%s", table.CommonTimebase)
	}
}

func TestMappingErrorsHavePositionAndCodeClass(t *testing.T) {
	badTB := clock.TickRate(44100)
	cases := [][]maptrack.Rule{
		{{Input: "missing.mp4", TrackID: 1, TargetID: 1}},
		{{Input: "a.mp4", TrackID: 99, TargetID: 1}},
		{
			{Input: "a.mp4", TrackID: 1, TargetID: 1},
			{Input: "a.mp4", TrackID: 1, TargetID: 2},
		},
		{
			{Input: "a.mp4", TrackID: 1, TargetID: 1},
			{Input: "a.mp4", TrackID: 2, TargetID: 1},
		},
		{{Input: "a.mp4", TrackID: 2, TargetID: 1, RequireTB: &badTB}},
	}
	for i, rules := range cases {
		_, err := maptrack.Apply(catalog(), rules)
		if err == nil {
			t.Fatalf("case %d expected error", i)
		}
		fe, ok := fault.As(err)
		if !ok || fe.Kind != fault.KindMapping || fe.Position == "" {
			t.Fatalf("case %d bad error: %#v", i, err)
		}
		if fault.ExitCode(err) != 2 {
			t.Fatalf("case %d exit=%d", i, fault.ExitCode(err))
		}
	}
}
