package probe

import (
	"testing"

	"tracecheck/internal/clock"
)

func TestParseTracksResolvesTwoRepresentations(t *testing.T) {
	f := demoFile()
	nodes, scanFaults := Scan(f.Data)
	if len(scanFaults) != 0 {
		t.Fatalf("scan faults: %v", scanFaults)
	}
	tracks, faults := ParseTracks(f.Data, nodes)
	if len(faults) != 0 {
		t.Fatalf("parse faults: %v", faults)
	}
	if len(tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(tracks))
	}
	first := tracks[0]
	if first.ID != 1 || first.Timescale != 48000 || first.Handler != "soun" {
		t.Fatalf("track header wrong: %+v", first)
	}
	if len(first.Samples) != 8 {
		t.Fatalf("samples = %d, want 8", len(first.Samples))
	}
	for i, s := range first.Samples {
		loc := f.TrackMdat[1][i]
		if int64(s.Offset) != int64(loc.Offset) || s.Size != int64(loc.Size) {
			t.Fatalf("sample %d offset/size = %d/%d want %d/%d",
				i, s.Offset, s.Size, loc.Offset, loc.Size)
		}
		if s.Delta != 480 {
			t.Fatalf("delta = %d, want 480", s.Delta)
		}
		wantSync := i%4 == 0
		if s.Sync != wantSync {
			t.Fatalf("sample %d sync = %v, want %v", i, s.Sync, wantSync)
		}
	}
	if first.Converter.Timescale() != 48000 {
		t.Fatal("converter not attached to track")
	}
}

func TestParseTracksReportsMissingIndex(t *testing.T) {
	f := demoFile()
	off := f.FindBoxHeaderForTrack("stco", 0)
	bad := f.RemoveBox(off)
	nodes, _ := Scan(bad)
	tracks, faults := ParseTracks(bad, nodes)
	var sawMissing bool
	for _, fe := range faults {
		if fe.Category == clock.CategoryMissingIndex {
			sawMissing = true
		}
	}
	if !sawMissing {
		t.Fatalf("missing-index not reported: %v", faults)
	}
	if len(tracks) != 2 || len(tracks[0].MissingIndex) == 0 {
		t.Fatalf("track not retained for recovery: %+v", tracks)
	}
}

func TestParseTracksTimelineStaysOnSharedClock(t *testing.T) {
	f := demoFile()
	nodes, _ := Scan(f.Data)
	tracks, _ := ParseTracks(f.Data, nodes)
	tr := tracks[0]
	// 480 units at 48 kHz = 10 ms = exactly 900 shared ticks.
	tick, err := tr.Converter.ConvertExact(480)
	if err != nil || tick != clock.Tick(900) {
		t.Fatalf("conversion = %d, %v; want 900", tick, err)
	}
}
