package packager

import (
	"strings"
	"testing"

	"mediaqc/internal/clock"
	"mediaqc/internal/model"
)

func mkEvent(seq uint64, startTicks, durTicks int64) model.Event {
	tb, _ := clock.New(90000)
	return model.Event{
		MaterialID: "mat-pkg", TrackID: "v", Sequence: seq, Timebase: tb,
		Start: tb.At(startTicks), Duration: tb.At(durTicks), PTS: tb.At(startTicks),
	}
}

func TestManifestFromEvents(t *testing.T) {
	b := NewBuilder("mat-pkg")
	evs := []model.Event{
		mkEvent(1, 0, 360000),    // 4000 ms
		mkEvent(2, 360000, 360000),
	}
	m, err := b.FromEvents(evs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 2 || m.Segments[1].StartMs != 4000 {
		t.Fatalf("segment boundaries wrong: %+v", m.Segments)
	}
	rendered := m.RenderHLS()
	if !strings.HasPrefix(rendered, "#EXTM3U") || !strings.Contains(rendered, "#EXT-X-ENDLIST") {
		t.Fatal("playlist must have header and endlist")
	}
	if !strings.Contains(rendered, "mat-pkg-seg-1.m4s") {
		t.Fatal("segment URI missing")
	}
}

func TestManifestHashDeterministic(t *testing.T) {
	build := func() Manifest {
		m, err := NewBuilder("mat-hash").FromEvents([]model.Event{
			mkEvent(0, 0, 90000), mkEvent(1, 90000, 90000),
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	if build().SHA256() != build().SHA256() {
		t.Fatal("identical manifests must hash identically")
	}
}

func TestRecoveredSegmentMarkedAndGapsPropagate(t *testing.T) {
	evs := []model.Event{mkEvent(0, 0, 90000), mkEvent(1, 90000, 90000)}
	evs[1].Recovered = true
	gaps := []model.Gap{{FirstSeq: 1, LastSeq: 1, TrackID: "v", Recovered: true}}
	m, err := NewBuilder("mat-rec").FromEvents(evs, gaps)
	if err != nil {
		t.Fatal(err)
	}
	rendered := m.RenderHLS()
	if !strings.Contains(rendered, "#EXT-X-DISCONTINUITY") {
		t.Fatal("recovered segment must be marked with discontinuity")
	}
}

func TestUnrecoveredGapRecordedInManifest(t *testing.T) {
	m, err := NewBuilder("mat-gap").FromEvents([]model.Event{mkEvent(0, 0, 90000)},
		[]model.Gap{{FirstSeq: 1, LastSeq: 2, TrackID: "v"}})
	if err != nil {
		t.Fatal(err)
	}
	rendered := m.RenderHLS()
	if !strings.Contains(rendered, "unrecovered_gaps=1") {
		t.Fatalf("manifest must declare unrecovered gaps: %s", rendered)
	}
}

func TestRejectsEventWithoutTimebase(t *testing.T) {
	bad := model.Event{MaterialID: "m", Sequence: 0} // zero/invalid timebase
	_, err := NewBuilder("mat-bad").FromEvents([]model.Event{bad}, nil)
	if err == nil || !model.HasCode(err, model.CodeZeroTimescale) {
		t.Fatalf("want zero-timescale error, got %v", err)
	}
}

func TestOutputSortedBySequence(t *testing.T) {
	evs := []model.Event{mkEvent(2, 180000, 90000), mkEvent(0, 0, 90000), mkEvent(1, 90000, 90000)}
	m, err := NewBuilder("mat-sort").FromEvents(evs, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(m.Segments); i++ {
		if m.Segments[i].Sequence < m.Segments[i-1].Sequence {
			t.Fatal("output segments must be sorted by sequence")
		}
	}
}
