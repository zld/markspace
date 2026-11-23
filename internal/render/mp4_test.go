package render

import (
	"bytes"
	"testing"

	"github.com/abema/go-mp4"
)

func TestBuildManifestBoxesDeterministic(t *testing.T) {
	frames, err := RenderFrames(nil, 0, 80_000_000, DefaultFrameOptions())
	if err != nil {
		t.Fatal(err)
	}
	hashes := HashFrames(frames)

	box1, sum1, err := BuildManifestBoxes(hashes)
	if err != nil {
		t.Fatal(err)
	}
	box2, sum2, err := BuildManifestBoxes(hashes)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(box1, box2) {
		t.Fatal("manifest boxes must be byte-identical across runs")
	}
	if len(sum1) != 1 || sum1[0].Type != "mdat" || sum1[0].Size != len(box1) {
		t.Fatalf("bad box summary: %+v", sum1)
	}
	if sum2[0].Type != sum1[0].Type || sum2[0].Size != sum1[0].Size {
		t.Fatal("summary mismatch across runs")
	}

	// The serialized box must be parseable and carry an mdat type marker.
	if _, err := mp4.ReadBoxStructure(bytes.NewReader(box1), func(h *mp4.ReadHandle) (interface{}, error) {
		if h.BoxInfo.Type != mp4.BoxTypeMdat() {
			t.Fatalf("expected mdat box, got %s", h.BoxInfo.Type.String())
		}
		return nil, nil
	}); err != nil {
		t.Fatalf("manifest box not parseable: %v", err)
	}
}
