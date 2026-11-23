package export

import (
	"encoding/json"
	"testing"

	"mediaroute/internal/codec"
	"mediaroute/internal/timeline"
)

func mkFrame(track uint32, sample uint32, usec int64, seg string, key bool) *timeline.Frame {
	return &timeline.Frame{
		Track: track, Codec: codec.H264, Sample: sample, Usec: usec,
		RTPTS: uint32(usec), Keyframe: key, Complete: true, SegmentID: seg,
		Bytes: []byte{byte(track), byte(sample), 0xA5},
	}
}

func TestBuildAndDeterminism(t *testing.T) {
	frames := []*timeline.Frame{
		mkFrame(1, 1, 1000, "seg-0", true),
		mkFrame(2, 1, 2000, "seg-0", false),
		mkFrame(1, 2, 3000, "seg-1", false),
	}
	m1, err := Build(frames)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := Build(frames)
	if err != nil {
		t.Fatal(err)
	}
	if len(m1.Segments) != 2 || m1.Segments[0].SegmentID != "seg-0" {
		t.Fatalf("segment grouping wrong: %+v", m1.Segments)
	}
	if m1.Segments[0].FrameCount != 2 || m1.Segments[1].FrameCount != 1 {
		t.Fatalf("frame counts wrong: %+v", m1.Segments)
	}
	h1, err := m1.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := m2.ContentHash()
	if h1 != h2 {
		t.Fatal("identical input produced different manifest hash")
	}
	for i := range m1.Segments {
		if m1.Segments[i].SHA256 != m2.Segments[i].SHA256 {
			t.Fatal("segment hash differs across identical builds")
		}
	}
}

func TestFrameOrderChangesContent(t *testing.T) {
	a := []*timeline.Frame{mkFrame(1, 1, 1000, "seg-0", true), mkFrame(1, 2, 2000, "seg-0", false)}
	b := []*timeline.Frame{a[1], a[0]}
	ma, _ := Build(a)
	mb, _ := Build(b)
	ha, _ := ma.ContentHash()
	hb, _ := mb.ContentHash()
	if ha == hb {
		t.Fatal("reordering frames did not change manifest hash")
	}
}

func TestRejectsUncommittedFrame(t *testing.T) {
	bad := mkFrame(1, 1, 1000, "", true)
	if _, err := Build([]*timeline.Frame{bad}); err == nil {
		t.Fatal("frame without committed segment was exported")
	}
}

func TestManifestJSONShape(t *testing.T) {
	m, err := Build([]*timeline.Frame{mkFrame(1, 1, 1000, "seg-0", true)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := m.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if _, ok := decoded["segments"]; !ok {
		t.Fatal("manifest missing segments key")
	}
	if len(m.SegmentHashes()) != 1 {
		t.Fatal("segment hash map wrong size")
	}
}

func TestFrameHashStable(t *testing.T) {
	m, _ := Build([]*timeline.Frame{mkFrame(1, 1, 1000, "seg-0", true)})
	want := hashBytes([]byte{1, 1, 0xA5})
	if m.Records[0].FrameSHA != want {
		t.Fatalf("frame sha mismatch: %s want %s", m.Records[0].FrameSHA, want)
	}
}
