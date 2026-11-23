package fixture

import (
	"bytes"
	"testing"

	"github.com/abema/go-mp4"

	"mediacheck/internal/model"
)

func TestGenerateDeterministic(t *testing.T) {
	a := Generate(17, nil)
	b := Generate(17, nil)
	if len(a.Video) != 3 || len(a.Audio) != 2 {
		t.Fatalf("unexpected ladder: %d video %d audio", len(a.Video), len(a.Audio))
	}
	if err := Materialize(a); err != nil {
		t.Fatalf("materialize a: %v", err)
	}
	if err := Materialize(b); err != nil {
		t.Fatalf("materialize b: %v", err)
	}
	for i := range a.Video {
		if !bytes.Equal(a.Video[i].Init, b.Video[i].Init) {
			t.Fatalf("init segment not deterministic for %s", a.Video[i].ID)
		}
		for j := range a.Video[i].Segments {
			if !bytes.Equal(a.Video[i].Segments[j].Data, b.Video[i].Segments[j].Data) {
				t.Fatalf("media segment not deterministic %s/%d", a.Video[i].ID, j)
			}
		}
	}
}

func TestFaultInjection(t *testing.T) {
	p := Generate(17, []model.FaultKind{
		model.FaultTruncate, model.FaultLate, model.FaultMetaConflict,
	})
	if err := Materialize(p); err != nil {
		t.Fatal(err)
	}
	if !p.Video[1].Segments[3].Truncated {
		t.Fatal("truncation fault missing")
	}
	if !p.Video[0].Segments[5].Late {
		t.Fatal("late fault missing")
	}
	if p.Video[2].Meta.ColorPrimaries != 9 || p.Audio[1].Meta.Channels != 6 {
		t.Fatal("metadata conflicts missing")
	}
	if p.Audio[1].Meta.SampleRate != 44100 {
		t.Fatal("audio sample rate conflict missing")
	}
}

func TestBuiltSegmentsAreParseable(t *testing.T) {
	p := Generate(17, nil)
	if err := Materialize(p); err != nil {
		t.Fatal(err)
	}
	r := p.Video[0]
	// The media segment must expose the standard fMP4 box types.
	_, err := mp4.ReadBoxStructure(bytes.NewReader(r.Segments[0].Data),
		func(h *mp4.ReadHandle) (interface{}, error) {
			return h.Expand()
		})
	if err != nil {
		t.Fatalf("go-mp4 cannot parse built segment: %v", err)
	}
	if _, err := mp4.ReadBoxStructure(bytes.NewReader(r.Init),
		func(h *mp4.ReadHandle) (interface{}, error) {
			return h.Expand()
		}); err != nil {
		t.Fatalf("go-mp4 cannot parse built init: %v", err)
	}
	if len(r.Segments[0].Packets) == 0 {
		t.Fatal("RTP packets missing")
	}
}
