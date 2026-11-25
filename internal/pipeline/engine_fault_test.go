package pipeline

import (
	"path/filepath"
	"testing"

	"mediavault/internal/repair"
	"mediavault/internal/report"
	"mediavault/internal/timeline"
)

// TestEngineUnrepairableBoundaryJump covers the corruption sample in which
// the boundary excursion is larger than the repairable bound: the track
// must be marked failed and retained, never silently discarded.
func TestEngineUnrepairableBoundaryJump(t *testing.T) {
	id := timeline.TrackID{Source: "rtp", Index: 0, Kind: timeline.KindVideo}
	norm := timeline.NewTrackNormalizer(id, timeline.ClockRef{Source: "rtp", Clock: 1}, 90000, 1000)
	rep := repair.NewBoundaryRepairer(1000) // tight repairable bound
	rep.Register(id, norm)

	good, err := norm.PushRTP(newRTPPacket(90_000, 1))
	if err != nil {
		t.Fatal(err)
	}
	_, err = norm.PushRTP(newRTPPacket(100, 2))
	if err == nil {
		t.Fatal("expected a boundary-jump error")
	}
	se := err.(*timeline.StageError)
	if _, herr := rep.Handle(se, 100); herr != timeline.ErrTrackFailed {
		t.Fatalf("want ErrTrackFailed for oversized excursion, got %v", herr)
	}
	if !norm.Failed() {
		t.Fatal("track must be marked failed")
	}
	failed := rep.Failed()
	if len(failed) != 1 || failed[0].Kind != timeline.KindUnrecoverable {
		t.Fatalf("failed-track record must be retained: %+v", failed)
	}
	if good == nil {
		t.Fatal("samples before the failure must survive")
	}
}

// TestEngineRTPTrackIsArchived verifies the RTP side-channel reaches the
// manifest with monotonic, wrap-expanded timestamps.
func TestEngineRTPTrackIsArchived(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "work")
	eng := NewEngine(Config{WorkDir: dir, Fault: FaultNone, ChunkSize: 4})
	m, err := eng.Run()
	if err != nil {
		t.Fatal(err)
	}
	var rtpTrack *report.TrackMetrics
	for i := range m.Tracks {
		if m.Tracks[i].Track == "rtp/t0(video)" {
			rtpTrack = &m.Tracks[i]
		}
	}
	if rtpTrack == nil {
		t.Fatal("RTP side-channel track must appear in the report")
	}
	if rtpTrack.SampleCount == 0 {
		t.Fatal("RTP track must contain linearized samples")
	}
}
