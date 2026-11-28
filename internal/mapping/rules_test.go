package mapping

import (
	"bytes"
	"testing"

	"mediacheck/internal/demux"
	merr "mediacheck/internal/errors"
	"mediacheck/internal/testmedia"
)

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

func parseTracks(t *testing.T, seed uint64, mutate func(*testmedia.MediaSpec)) []*demux.Track {
	t.Helper()
	spec := testmedia.DefaultSpec(seed)
	if mutate != nil {
		mutate(&spec)
	}
	media, err := testmedia.BuildMP4(spec)
	if err != nil {
		t.Fatal(err)
	}
	c, err := demux.Parse(bytesReader(media.Container))
	if err != nil {
		t.Fatal(err)
	}
	return c.Tracks
}

func TestApplyHealthy(t *testing.T) {
	tracks := parseTracks(t, 7, nil)
	rep := Apply(tracks, DefaultRules(tracks))
	if !rep.OK() {
		t.Fatalf("unexpected conflicts: %+v", rep.Conflicts)
	}
	if len(rep.Mapped) != 2 || rep.Mapped[0].Kind != demux.KindVideo {
		t.Fatalf("mapped order wrong: %+v", rep.Mapped)
	}
}

func TestMissingTrackStopsAsset(t *testing.T) {
	tracks := parseTracks(t, 7, nil)
	rules := []Rule{
		{SourceTrackID: 99, TargetSlot: 0, RequireKind: demux.KindVideo},
	}
	rep := Apply(tracks, rules)
	if rep.OK() || rep.Mapped != nil {
		t.Fatal("missing source must fail and produce no mapping")
	}
	if rep.Conflicts[0].Code != merr.CodeMissingTrack {
		t.Fatalf("code = %q", rep.Conflicts[0].Code)
	}
}

func TestDuplicateTargetReported(t *testing.T) {
	tracks := parseTracks(t, 7, nil)
	rules := []Rule{
		{SourceTrackID: 1, TargetSlot: 0},
		{SourceTrackID: 2, TargetSlot: 0},
	}
	rep := Apply(tracks, rules)
	if rep.OK() {
		t.Fatal("duplicate target must fail")
	}
	found := false
	for _, c := range rep.Conflicts {
		if c.Code == merr.CodeDuplicateTarget {
			found = true
		}
	}
	if !found {
		t.Fatalf("want duplicate-target conflict, got %+v", rep.Conflicts)
	}
}

func TestCodecIncompatibility(t *testing.T) {
	tracks := parseTracks(t, 7, func(s *testmedia.MediaSpec) {
		s.Tracks[0].Codec = "zyz1"
	})
	rules := []Rule{
		{SourceTrackID: 1, TargetSlot: 0, RequireCodec: "avc1"},
	}
	rep := Apply(tracks, rules)
	if rep.OK() {
		t.Fatal("unknown codec must fail")
	}
	if rep.Conflicts[0].Code != merr.CodeUnsupportedTrack {
		t.Fatalf("code = %q", rep.Conflicts[0].Code)
	}
}

func TestAllConflictsCollectedInOnePass(t *testing.T) {
	tracks := parseTracks(t, 7, nil)
	rules := []Rule{
		{SourceTrackID: 41, TargetSlot: 0},
		{SourceTrackID: 42, TargetSlot: 1},
	}
	rep := Apply(tracks, rules)
	if len(rep.Conflicts) != 2 {
		t.Fatalf("conflicts = %d; want 2 reported together", len(rep.Conflicts))
	}
}
