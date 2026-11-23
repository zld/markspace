package mapping

import (
	"reflect"
	"strings"
	"testing"

	"mediatool/internal/mp4"
	"mediatool/internal/timeline"
)

func sampleTracks() []mp4.TrackInfo {
	return []mp4.TrackInfo{
		{TrackID: 1, Timescale: 90000, MediaType: "video", CodecKey: "avc1"},
		{TrackID: 2, Timescale: 48000, MediaType: "audio", CodecKey: "mp4a"},
		{TrackID: 3, Timescale: 48000, MediaType: "audio", CodecKey: "ac-3"},
	}
}

func TestMapExplicitTrackIDs(t *testing.T) {
	m := NewMapper([]Rule{
		{OutputSlot: "video-main", TrackID: 1},
		{OutputSlot: "audio-main", TrackID: 2},
	})
	m.SetTracks(sampleTracks())
	slots := m.Recompute()
	if len(slots) != 2 || !slots[0].Resolved || !slots[1].Resolved {
		t.Fatalf("both rules must resolve: %+v", slots)
	}
	if slots[0].Track.TrackID != 1 || slots[1].Track.TrackID != 2 {
		t.Fatalf("wrong track binding: %+v", slots)
	}
	if len(m.Failures()) != 0 {
		t.Fatal("resolved mapping must have no failures")
	}
}

func TestMapByTypeAndCodec(t *testing.T) {
	m := NewMapper([]Rule{
		{OutputSlot: "any-video", MediaType: "video"},
		{OutputSlot: "aac-audio", MediaType: "audio", CodecKey: "mp4a"},
	})
	m.SetTracks(sampleTracks())
	slots := m.Recompute()
	if !slots[0].Resolved || slots[0].Track.TrackID != 1 {
		t.Fatalf("video rule wrong: %+v", slots[0])
	}
	if !slots[1].Resolved || slots[1].Track.TrackID != 2 {
		t.Fatalf("codec-pinned audio rule wrong: %+v", slots[1])
	}
}

func TestMapMissingTrack(t *testing.T) {
	m := NewMapper([]Rule{{OutputSlot: "ghost", TrackID: 99}})
	m.SetTracks(sampleTracks())
	slots := m.Recompute()
	if slots[0].Resolved {
		t.Fatal("missing track must not resolve")
	}
	fails := m.Failures()
	if len(fails) != 1 || fails[0].Class != timeline.ClassMappingConflict {
		t.Fatalf("want one mapping conflict, got %+v", fails)
	}
}

func TestMapCodecConflict(t *testing.T) {
	m := NewMapper([]Rule{
		{OutputSlot: "video-hevc", TrackID: 1, CodecKey: "hvc1"},
	})
	m.SetTracks(sampleTracks())
	slots := m.Recompute()
	if slots[0].Resolved {
		t.Fatal("codec mismatch must block resolution")
	}
	if diag := slots[0].Diagnosis; !strings.Contains(diag, "codec") {
		t.Fatalf("diagnosis must explain codec conflict: %q", diag)
	}
}

func TestMapAmbiguousMultiTrack(t *testing.T) {
	// Type-only audio rule matches two tracks.
	m := NewMapper([]Rule{{OutputSlot: "which-audio", MediaType: "audio"}})
	m.SetTracks(sampleTracks())
	slots := m.Recompute()
	if slots[0].Resolved {
		t.Fatal("ambiguous rule must not silently pick a track")
	}
	if !strings.Contains(slots[0].Diagnosis, "conflict") {
		t.Fatalf("want conflict diagnosis, got %q", slots[0].Diagnosis)
	}
}

func TestMapDoubleAssignmentConflict(t *testing.T) {
	// Two explicit rules target the same track id.
	m := NewMapper([]Rule{
		{OutputSlot: "first", TrackID: 1},
		{OutputSlot: "second", TrackID: 1},
	})
	m.SetTracks(sampleTracks())
	slots := m.Recompute()
	if !slots[0].Resolved || slots[1].Resolved {
		t.Fatalf("first rule wins, second must fail: %+v", slots)
	}
	if !strings.Contains(slots[1].Diagnosis, "already assigned") {
		t.Fatalf("want double-assignment diagnosis, got %q", slots[1].Diagnosis)
	}
}

func TestMapRecomputeIdempotent(t *testing.T) {
	m := NewMapper([]Rule{
		{OutputSlot: "v", MediaType: "video"},
		{OutputSlot: "missing", TrackID: 42},
	})
	m.SetTracks(sampleTracks())
	first := m.Recompute()
	second := m.Recompute()
	if !reflect.DeepEqual(first, second) {
		t.Fatal("repeated recompute must be identical")
	}
	resolved := m.ResolvedSlots()
	if len(resolved) != 1 || resolved[0].Rule.OutputSlot != "v" {
		t.Fatalf("resolved slots wrong: %+v", resolved)
	}
}
