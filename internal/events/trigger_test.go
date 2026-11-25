package events

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"replay/internal/cmaf"
	"replay/internal/model"
)

func timeline(end time.Duration, missing ...cmaf.Interval) *cmaf.Timeline {
	return &cmaf.Timeline{End: end, Missing: missing}
}

func TestLoadAndFind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.json")
	data := `{"events":[{"id":"kill-1","type":"kill","time_ms":12000},{"id":"kill-2","type":"kill","time_ms":15000}]}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	evs, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("got %d events", len(evs))
	}
	ev := Find(evs, "kill-2")
	if ev == nil || ev.Time() != 15*time.Second {
		t.Fatalf("bad find: %+v", ev)
	}
	if Find(evs, "nope") != nil {
		t.Fatal("expected nil for unknown id")
	}
}

func TestLoadRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.json")
	if err := os.WriteFile(path, []byte(`{"events":[{"id":"","time_ms":1}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for empty id")
	}
}

func TestWindowBasic(t *testing.T) {
	rep := &model.Report{}
	w, err := WindowFor(Event{ID: "k", TimeMS: 12000}, 3*time.Second, 5*time.Second,
		timeline(20*time.Second), 500*time.Millisecond, rep)
	if err != nil {
		t.Fatal(err)
	}
	if w.Start != 9*time.Second || w.End != 17*time.Second {
		t.Fatalf("window = [%v, %v)", w.Start, w.End)
	}
	if len(rep.Issues) != 0 {
		t.Fatalf("unexpected issues: %+v", rep.Issues)
	}
}

func TestWindowClampsToZero(t *testing.T) {
	rep := &model.Report{}
	w, err := WindowFor(Event{ID: "k", TimeMS: 1000}, 3*time.Second, 5*time.Second,
		timeline(20*time.Second), 500*time.Millisecond, rep)
	if err != nil {
		t.Fatal(err)
	}
	if w.Start != 0 {
		t.Fatalf("start = %v, want 0", w.Start)
	}
}

func TestWindowRejectsBeyondEnd(t *testing.T) {
	rep := &model.Report{}
	_, err := WindowFor(Event{ID: "k", TimeMS: 25000}, 3*time.Second, 5*time.Second,
		timeline(20*time.Second), 500*time.Millisecond, rep)
	if !errors.Is(err, model.ErrEventOutOfRange) {
		t.Fatalf("want ErrEventOutOfRange, got %v", err)
	}
}

func TestWindowAcceptsSmallOvershoot(t *testing.T) {
	rep := &model.Report{}
	w, err := WindowFor(Event{ID: "k", TimeMS: 20300}, 3*time.Second, 5*time.Second,
		timeline(20*time.Second), 500*time.Millisecond, rep)
	if err != nil {
		t.Fatal(err)
	}
	if w.End != 20*time.Second {
		t.Fatalf("end = %v, want clamped 20s", w.End)
	}
	if rep.Count(model.CodeEventMismatch) != 1 {
		t.Fatal("expected EVENT_MISMATCH issue")
	}
}

func TestWindowRejectsLargeMissingGap(t *testing.T) {
	tl := timeline(20*time.Second, cmaf.Interval{Start: 10 * time.Second, End: 14 * time.Second})
	rep := &model.Report{}
	// event at 12s sits in the middle of a 4s hole: 2s from either edge.
	_, err := WindowFor(Event{ID: "k", TimeMS: 12000}, 3*time.Second, 5*time.Second,
		tl, 500*time.Millisecond, rep)
	if !errors.Is(err, model.ErrEventOutOfRange) {
		t.Fatalf("want ErrEventOutOfRange, got %v", err)
	}
}

func TestWindowClipsAroundSmallGap(t *testing.T) {
	tl := timeline(20*time.Second, cmaf.Interval{Start: 12300 * time.Millisecond, End: 12700 * time.Millisecond})
	rep := &model.Report{}
	// hole edge is 300ms after the event: within tolerance, window clipped.
	w, err := WindowFor(Event{ID: "k", TimeMS: 12000}, 3*time.Second, 5*time.Second,
		tl, 500*time.Millisecond, rep)
	if err != nil {
		t.Fatal(err)
	}
	if w.End != 12300*time.Millisecond {
		t.Fatalf("end = %v, want 12.3s", w.End)
	}
	if rep.Count(model.CodeEventMismatch) != 1 {
		t.Fatal("expected EVENT_MISMATCH issue")
	}
}
