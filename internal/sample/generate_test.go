package sample

import (
	"strings"
	"testing"

	"mediasync/internal/dash"
	"mediasync/internal/hls"
	"mediasync/internal/model"
)

func TestBuildAllDeterministic(t *testing.T) {
	params := Params{LossPct: 20, JitterMS: 3}
	first := BuildAll(17, params)
	second := BuildAll(17, params)
	if len(first) != len(second) {
		t.Fatalf("lane count differs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		a, b := first[i], second[i]
		if a.Representation != b.Representation || len(a.Segments) != len(b.Segments) {
			t.Fatalf("lane %d structure differs across runs", i)
		}
		for j := range a.Segments {
			if a.Segments[j].StartNS != b.Segments[j].StartNS ||
				string(a.Segments[j].Payload) != string(b.Segments[j].Payload) {
				t.Fatalf("lane %s segment %d differs across runs", a.Representation, j)
			}
		}
		if a.Recovery != b.Recovery || a.BufferStats != b.BufferStats {
			t.Fatalf("lane %s RTP evidence differs across runs", a.Representation)
		}
	}
}

func TestDifferentSeedsProduceDifferentStreams(t *testing.T) {
	a := BuildAll(1, Params{LossPct: 30, JitterMS: 4})
	b := BuildAll(2, Params{LossPct: 30, JitterMS: 4})
	different := false
	for i := range a {
		if a[i].BufferStats.OutOfOrder != b[i].BufferStats.OutOfOrder ||
			a[i].Recovery.Lost != b[i].Recovery.Lost {
			different = true
		}
	}
	if !different {
		t.Fatal("different seeds should produce different loss/jitter patterns")
	}
}

func TestCorruptLaneMarkedAndHealthyLanesWhole(t *testing.T) {
	lanes := BuildAll(17, Params{LossPct: 10, JitterMS: 2})
	var corruptCount, coreCount int
	for _, lane := range lanes {
		if lane.CorruptMP4 {
			corruptCount++
		}
		if lane.Priority == model.PriorityCore {
			coreCount++
			if lane.Representation == "" || len(lane.MP4Tracks) == 0 {
				t.Fatalf("core lane %s must remain fully specified", lane.Representation)
			}
		}
	}
	if corruptCount != 1 || coreCount != 2 {
		t.Fatalf("want exactly 1 corrupt and 2 core lanes, got corrupt=%d core=%d",
			corruptCount, coreCount)
	}
}

func TestIllegalTimescaleAndDiscontinuityFixtures(t *testing.T) {
	m := dash.Parse(IllegalTimescaleManifest())
	if len(m.Errors) == 0 || model.Classify(m.Errors[0]) != model.KindInvalidTimescale {
		t.Fatalf("illegal timescale fixture must be rejected: %+v", m.Errors)
	}
	playlist, err := hls.Parse(DiscontinuityPlaylist(), 90000)
	if err != nil {
		t.Fatal(err)
	}
	if playlist.DiscoCount != 2 {
		t.Fatalf("discontinuity count = %d, want 2", playlist.DiscoCount)
	}
	if !strings.Contains(string(DiscontinuityPlaylist()), "#EXT-X-DISCONTINUITY") {
		t.Fatal("discontinuity marker missing from fixture")
	}
}
