package scan

import (
	"os"
	"path/filepath"
	"testing"

	"mediapipeline/internal/fixture"
)

func TestReadRTPStreamBoundaries(t *testing.T) {
	descs := generate(t, fixture.CaseNormal, 1, 4)
	boundary, err := ReadRTPStream(filepath.Join(descs[0].Directory, fixture.RTPFileName))
	if err != nil {
		t.Fatal(err)
	}
	if boundary.PacketCount != 4 {
		t.Fatalf("packet count = %d, want 4", boundary.PacketCount)
	}
	if boundary.FirstSequence != 0 || boundary.LastSequence != 3 {
		t.Fatalf("sequence span = [%d,%d], want [0,3]", boundary.FirstSequence, boundary.LastSequence)
	}
	if boundary.FirstTimestamp != 0 {
		t.Fatalf("first timestamp = %d, want 0", boundary.FirstTimestamp)
	}
	if len(boundary.SequenceGaps) != 0 {
		t.Fatalf("healthy stream must have no gaps, got %v", boundary.SequenceGaps)
	}
	if len(boundary.Markers) != 4 {
		t.Fatalf("markers = %v", boundary.Markers)
	}
	if boundary.Markers[0] != 1 || boundary.Markers[3] != 4 {
		t.Fatalf("mfhd markers wrong: %v", boundary.Markers)
	}
}

func TestRTPMarkersTrackDuplicateArrival(t *testing.T) {
	descs := generate(t, fixture.CaseDuplicateFold, 1, 5)
	boundary, err := ReadRTPStream(filepath.Join(descs[0].Directory, fixture.RTPFileName))
	if err != nil {
		t.Fatal(err)
	}
	// Five canonical fragments plus one duplicate arrival.
	if boundary.PacketCount != 6 {
		t.Fatalf("packet count = %d, want 6", boundary.PacketCount)
	}
	// Markers map input index -> mfhd; the final arrival repeats mfhd 4.
	if boundary.Markers[3] != 4 || boundary.Markers[4] != 5 || boundary.Markers[5] != 4 {
		t.Fatalf("duplicate marker should repeat mfhd 4 on the last arrival: %v", boundary.Markers)
	}
}

func writeTestFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
