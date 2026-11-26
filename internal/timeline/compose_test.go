package timeline

import "testing"

func TestComposeSortsAlignsAndMarksSwitch(t *testing.T) {
	in := []TrackSample{
		{Kind: "audio", Track: "a", PTSNS: 5_000_000, Source: "audio.m4s"},
		{Kind: "video", Track: "v", PTSNS: 0, Source: "v0.m4s", Switch: true},
		{Kind: "video", Track: "v", PTSNS: 500_000_000, Source: "v1.m4s"},
		{Kind: "audio", Track: "a", PTSNS: 505_000_000, Source: "a1.m4s"},
		{Kind: "video", Track: "v", PTSNS: 0, Source: "v0.m4s"}, // duplicate
	}
	r, err := Compose(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Points) != 2 || r.Duplicates != 1 {
		t.Fatalf("points=%d dup=%d", len(r.Points), r.Duplicates)
	}
	if r.SyncMaxNS != 5_000_000 {
		t.Fatalf("sync max=%d", r.SyncMaxNS)
	}
	if !r.Points[0].Switch || len(r.SwitchBoundaries) != 1 {
		t.Fatalf("switch not recorded: %+v", r)
	}
}

func TestMissingTrackIsMarkedNotFilled(t *testing.T) {
	in := []TrackSample{{Kind: "video", PTSNS: 0, Source: "v"}}
	r, err := Compose(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Points[0].Gap || len(r.Points[0].Missing) != 1 || r.MissingSamples != 1 {
		t.Fatalf("point=%+v result=%+v", r.Points[0], r)
	}
}

func TestExcessiveSyncErrorFails(t *testing.T) {
	in := []TrackSample{
		{Kind: "video", PTSNS: 0},
		{Kind: "audio", PTSNS: 50_000_000},
	}
	r, err := Compose(in, Options{})
	if err == nil || r == nil || r.SyncMaxNS != 50_000_000 {
		t.Fatalf("r=%+v err=%v", r, err)
	}
}
