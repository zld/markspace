package timeline

import "testing"

func TestEditListDistinguishesGapAndMediaTime(t *testing.T) {
	l := EditList{MovieTimescale: 1000, MediaTimescale: 24000, Edits: []Edit{
		{SegmentDuration: 1000, MediaTime: -1},
		{SegmentDuration: 2000, MediaTime: 48000},
	}}
	gap, err := l.MapPresentation(Rat(500, 1000))
	if err != nil || !gap.InGap {
		t.Fatalf("expected gap, got %+v err=%v", gap, err)
	}
	m, err := l.MapPresentation(Rat(2, 1))
	if err != nil {
		t.Fatal(err)
	}
	// Presentation 2s is one second into the second edit; media starts at 2s.
	if m.InGap || m.Media.String() != "3" {
		t.Fatalf("unexpected mapping: %+v", m)
	}
}

func TestValidateEditRange(t *testing.T) {
	l := EditList{MovieTimescale: 1000, MediaTimescale: 24000, Edits: []Edit{
		{SegmentDuration: 3000, MediaTime: 200000},
	}}
	issues := l.ValidateMediaRange(240000)
	if len(issues) != 1 || issues[0].OverflowTicks <= 0 {
		t.Fatalf("expected overflow, got %+v", issues)
	}
}
