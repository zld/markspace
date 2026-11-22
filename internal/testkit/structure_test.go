package testkit

import (
	"os"
	"path/filepath"
	"testing"

	mp4 "github.com/abema/go-mp4"
)

func TestDumpStructure(t *testing.T) {
	dir := t.TempDir()
	spec := MP4Spec{
		Path: filepath.Join(dir, "t.mp4"), Manifest: filepath.Join(dir, "m.json"),
		TrackID: 1, Timescale: 90000, SampleCount: 10, SampleDelta: 3000,
		KeyEvery: 5, TargetBPS: 1_800_000, Seed: 1, MediaStartTicks: 90000,
	}
	if err := WriteMP4(spec); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(spec.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		indent := ""
		for i := 0; i < len(h.Path); i++ {
			indent += "  "
		}
		t.Logf("%s%s off=%d size=%d hdr=%d", indent, h.BoxInfo.Type.String(), h.BoxInfo.Offset, h.BoxInfo.Size, h.BoxInfo.HeaderSize)
		return h.Expand()
	})
	if err != nil {
		t.Fatal(err)
	}
}
