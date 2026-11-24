package fixture

import (
	"bytes"
	"testing"

	"github.com/abema/go-mp4"
)

// TestGenerateMP4RoundTrip ensures the hand-rolled writer produces a file
// the real go-mp4 parser accepts, with the expected box types and tables.
func TestGenerateMP4RoundTrip(t *testing.T) {
	spec := DefaultMP4Spec(7)
	spec.Rotation = 90
	data, manifest := GenerateMP4(spec)

	var (
		mdhdCount, stssCount, colrCount int
		videoKeyframes                  int
		sawTkhdRotation                 bool
	)
	_, err := mp4.ReadBoxStructure(bytes.NewReader(data), func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMdhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			md := box.(*mp4.Mdhd)
			if md.Timescale != 30000 && md.Timescale != 48000 {
				t.Errorf("unexpected timescale %d", md.Timescale)
			}
			mdhdCount++
		case mp4.BoxTypeStss():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			videoKeyframes = len(box.(*mp4.Stss).SampleNumber)
			stssCount++
		case mp4.BoxTypeColr():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			colr := box.(*mp4.Colr)
			if colr.ColourPrimaries != 1 {
				t.Errorf("colour primaries = %d, want 1", colr.ColourPrimaries)
			}
			colrCount++
		case mp4.BoxTypeTkhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			tk := box.(*mp4.Tkhd)
			if tk.TrackID == 1 && tk.Matrix[0] == 0 && tk.Matrix[1] == 0x00010000 {
				sawTkhdRotation = true
			}
		}
		// Container boxes must be expanded for the walk to descend.
		if h.BoxInfo.IsSupportedType() {
			if _, err := h.Expand(); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	if err != nil {
		t.Fatalf("go-mp4 rejected generated file: %v", err)
	}
	if mdhdCount != 2 {
		t.Errorf("mdhd count = %d, want 2", mdhdCount)
	}
	if stssCount != 1 || videoKeyframes != 6 { // frames 0,15,...,75
		t.Errorf("video keyframes = %d (stss boxes %d), want 6", videoKeyframes, stssCount)
	}
	if colrCount != 1 {
		t.Errorf("colr boxes = %d, want 1", colrCount)
	}
	if !sawTkhdRotation {
		t.Error("90-degree rotation matrix not found in video tkhd")
	}

	// Manifest consistency: offsets monotonic and inside the file.
	if len(manifest.Samples) != 90+180 {
		t.Fatalf("manifest samples = %d, want 270", len(manifest.Samples))
	}
	prev := int64(-1)
	for _, s := range manifest.Samples {
		if s.Offset <= prev {
			t.Fatalf("sample offsets not monotonic: %d after %d", s.Offset, prev)
		}
		if s.Offset+int64(s.Size) > int64(len(data)) {
			t.Fatalf("sample at %d extends past EOF %d", s.Offset, len(data))
		}
		prev = s.Offset
	}
}

func TestGenerateMP4Deterministic(t *testing.T) {
	a, ma := GenerateMP4(DefaultMP4Spec(7))
	b, mb := GenerateMP4(DefaultMP4Spec(7))
	if !bytes.Equal(a, b) {
		t.Fatal("same seed produced different bytes")
	}
	if len(ma.Samples) != len(mb.Samples) {
		t.Fatal("manifest length differs across runs")
	}
}
