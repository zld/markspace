package timeline

import (
	"errors"
	"testing"

	"replayctl/internal/fault"
)

func TestMapWindowFindsEmptySegment(t *testing.T) {
	tl := Timeline{Clips: []Clip{
		{ID: "a", StartMS: 0, EndMS: 800},
		{ID: "b", StartMS: 1200, EndMS: 2000},
	}}
	_, err := tl.MapWindow(500, 1500)
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Code != fault.TimeGap {
		t.Fatalf("err=%v, want TIME_GAP", err)
	}
}

func TestMapWindowMapsSourceIntervals(t *testing.T) {
	tl := Timeline{Clips: []Clip{
		{ID: "a", StartMS: 0, EndMS: 1100, SourceInMS: 0},
		{ID: "b", StartMS: 900, EndMS: 2000, SourceInMS: 5000},
	}}
	m, err := tl.MapWindow(500, 1500)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 2 {
		t.Fatalf("segments=%d", len(m.Segments))
	}
	if m.Segments[0].SourceStartMS != 500 || m.Segments[0].SourceEndMS != 1100 {
		t.Fatalf("A source=%d,%d", m.Segments[0].SourceStartMS, m.Segments[0].SourceEndMS)
	}
	if m.Segments[1].SourceStartMS != 5000 || m.Segments[1].SourceEndMS != 5600 {
		t.Fatalf("B source=%d,%d", m.Segments[1].SourceStartMS, m.Segments[1].SourceEndMS)
	}
}

func TestLinearMixAndSaturation(t *testing.T) {
	if got := MixLinear(0, 10, 1, 2); got != 5 {
		t.Fatalf("alpha half = %d, want 5", got)
	}
	if got := MixLinear(30000, 30000, 1, 2); got != 30000 {
		t.Fatalf("identical signals should preserve amplitude, got %d", got)
	}
	if got := LimitSample(40000); got != 32767 {
		t.Fatalf("positive saturation = %d", got)
	}
	if got := LimitSample(-40000); got != -32768 {
		t.Fatalf("negative saturation = %d", got)
	}
}
