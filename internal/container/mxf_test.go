package container

import (
	"strings"
	"testing"

	"github.com/local/medialab/internal/fixture"
	"github.com/local/medialab/internal/timeline"
)

func TestParseMXFHappyPath(t *testing.T) {
	data, manifest := fixture.GenerateMXF(fixture.DefaultMXFSpec(7))
	meta, err := ParseMXF(data)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if meta.Format != "mxf" || len(meta.Tracks) != 1 {
		t.Fatalf("unexpected meta: %+v", meta.FileMeta)
	}
	tr := meta.Tracks[0]
	if tr.Kind != "video" || tr.SampleCount != manifest.VideoFrames {
		t.Fatalf("track = %+v", tr)
	}
	if tr.FrameRateNum != 30000 || tr.FrameRateDen != 1001 {
		t.Fatalf("edit rate = %d/%d", tr.FrameRateNum, tr.FrameRateDen)
	}
	// One frame at 29.97fps is ~33.37ms; total ~3003ms.
	ms := timeline.Millis(tr.DurationTicks)
	if ms < 2990 || ms > 3010 {
		t.Fatalf("duration = %dms, want ~3003ms", ms)
	}
	if tr.Color == nil || tr.Color.Primaries != 6 || tr.Color.Source != "mxf" {
		t.Fatalf("color = %+v", tr.Color)
	}
	if tr.Width != 640 || tr.Height != 360 {
		t.Fatalf("dimensions = %dx%d", tr.Width, tr.Height)
	}
	// Keyframes every 15 frames: 1,16,31,46,61,76.
	want := []uint32{1, 16, 31, 46, 61, 76}
	if len(tr.KeyframeSampleNumbers) != len(want) {
		t.Fatalf("keyframes = %v, want %v", tr.KeyframeSampleNumbers, want)
	}
	for i := range want {
		if tr.KeyframeSampleNumbers[i] != want[i] {
			t.Fatalf("keyframe %d = %d, want %d", i, tr.KeyframeSampleNumbers[i], want[i])
		}
	}
	if meta.EssenceStart != manifest.EssenceOffset {
		t.Fatalf("essence offset = %d, want %d", meta.EssenceStart, manifest.EssenceOffset)
	}
}

func TestParseMXFMissingIndex(t *testing.T) {
	spec := fixture.DefaultMXFSpec(7)
	spec.DropIndex = true
	data, _ := fixture.GenerateMXF(spec)
	meta, err := ParseMXF(data)
	if err != nil {
		t.Fatalf("parse without index failed: %v", err)
	}
	if meta.HasIndex {
		t.Fatal("HasIndex should be false when the index pack is absent")
	}
	if len(meta.Tracks[0].KeyframeSampleNumbers) != 0 {
		t.Fatal("keyframes must be empty, not fabricated, without an index")
	}
}

func TestParseMXFTruncated(t *testing.T) {
	data, _ := fixture.GenerateMXF(fixture.DefaultMXFSpec(7))
	cut := data[:len(data)-25] // split the footer KLV
	if _, err := ParseMXF(cut); err == nil {
		t.Fatal("truncated KLV must be an error, not silent success")
	} else if !strings.Contains(err.Error(), "offset") {
		t.Fatalf("error must carry an offset: %v", err)
	}

	// Cutting inside a declared BER length must be detected as EOF too.
	if _, err := ParseMXF(data[:len(data)-8]); err == nil {
		t.Fatal("short value must be rejected")
	}
}
