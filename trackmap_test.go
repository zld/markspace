package mediacore

import "testing"

func TestTrackMapDefaultsPreserveIDs(t *testing.T) {
	p := genFixtures(t, 17)
	c, err := DemuxFile(p.MP4, DefaultDemuxOptions())
	if err != nil {
		t.Fatal(err)
	}
	tm, err := BuildTrackMap(c, DefaultScanOptions().Map)
	if err != nil {
		t.Fatalf("BuildTrackMap: %v", err)
	}
	if id, ok := tm.TargetIDFor(1); !ok || id != 1 {
		t.Fatalf("video target = %d,%t want 1,true", id, ok)
	}
	if id, ok := tm.TargetIDFor(2); !ok || id != 2 {
		t.Fatalf("audio target = %d,%t want 2,true", id, ok)
	}
}

func TestTrackMapExplicitSourceAndTarget(t *testing.T) {
	p := genFixtures(t, 17)
	c, _ := DemuxFile(p.MP4, DefaultDemuxOptions())
	cfg := TrackMapConfig{Rules: []TrackRule{
		{Kind: KindVideoTrack, SourceID: 1, TargetID: 10},
		{Kind: KindAudioTrack, SourceID: 2, TargetID: 20},
	}}
	tm, err := BuildTrackMap(c, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := tm.TargetIDFor(1); id != 10 {
		t.Fatalf("video -> %d, want 10", id)
	}
	if id, _ := tm.TargetIDFor(2); id != 20 {
		t.Fatalf("audio -> %d, want 20", id)
	}
}

func TestTrackMapTargetCollisionFails(t *testing.T) {
	p := genFixtures(t, 17)
	c, _ := DemuxFile(p.MP4, DefaultDemuxOptions())
	cfg := TrackMapConfig{Rules: []TrackRule{
		{Kind: KindVideoTrack, SourceID: 1, TargetID: 9},
		{Kind: KindAudioTrack, SourceID: 2, TargetID: 9},
	}}
	_, err := BuildTrackMap(c, cfg)
	if !IsKind(err, KindTrackMap) {
		t.Fatalf("collision expected KindTrackMap, got %v (%q)", err, KindOf(err))
	}
}

func TestTrackMapMissingKindFails(t *testing.T) {
	p := genFixtures(t, 17)
	c, _ := DemuxFile(p.MP4, DefaultDemuxOptions())
	cfg := TrackMapConfig{Rules: []TrackRule{
		{Kind: KindVideoTrack, SourceID: 1},
		{Kind: KindAudioTrack, SourceID: 99}, // no such source id
	}}
	if _, err := BuildTrackMap(c, cfg); !IsKind(err, KindTrackMap) {
		t.Fatalf("missing source expected KindTrackMap, got %v", err)
	}
}
