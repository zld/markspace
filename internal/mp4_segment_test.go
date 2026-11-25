package kernel

import (
	"bytes"
	"testing"

	"github.com/abema/go-mp4"
)

func TestPackageSegmentMP4HasProbeableISOBoxes(t *testing.T) {
	seg := &Segment{
		Index: 1, ID: "segment-001", Time: RangeMS(0, 1000),
		Tracks: []SegmentTrack{{TrackID: "audio", PID: 0x101, Kind: TrackAudio}},
	}
	data, err := PackageSegmentMP4(seg, "source-digest")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 16 || string(data[4:8]) != "ftyp" {
		t.Fatal("missing ftyp box")
	}
	info, err := mp4.Probe(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("go-mp4 probe: %v", err)
	}
	if string(info.MajorBrand[:]) != "isom" {
		t.Fatalf("major brand=%q", info.MajorBrand)
	}
}
