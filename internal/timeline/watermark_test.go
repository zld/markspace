package timeline

import (
	"testing"

	"archive.example/mediakernel/internal/events"
)

func newTestTimeline(t *testing.T, lateNS int64) *Timeline {
	t.Helper()
	tl, err := New(Config{LateWindowNS: lateNS, CheckpointEvery: 4, MaxReplayEvents: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

func sub(track events.TrackID, at int64, seq uint64, text string) events.Event {
	return events.Event{Track: track, Kind: events.KindSubtitle, At: at, Seq: seq, Text: text}
}

func admitAll(t *testing.T, tl *Timeline, evs []events.Event) {
	t.Helper()
	for _, e := range evs {
		if _, err := tl.Admit(e); err != nil {
			t.Fatalf("admit %v: %v", e.Identity(), err)
		}
	}
}

func TestWatermarkCommitsOnlyFinalEvents(t *testing.T) {
	tl := newTestTimeline(t, 1000)
	res, err := tl.Admit(sub(events.TrackSubtitle, 500, 1, "a"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Watermark != -500 {
		t.Fatalf("watermark %d", res.Watermark)
	}
	if len(res.Committed) != 0 {
		t.Fatalf("event before watermark must not commit: %+v", res.Committed)
	}
	if got := len(tl.Pending()); got != 1 {
		t.Fatalf("pending %d", got)
	}

	res, err = tl.Admit(sub(events.TrackSubtitle, 5000, 2, "b"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Watermark != 4000 {
		t.Fatalf("watermark %d", res.Watermark)
	}
	// The newest event (5000ns) is still inside the lateness window, so only
	// the event at 500ns is final once the watermark reaches 4000ns.
	if len(res.Committed) != 1 {
		t.Fatalf("committed %d want 1", len(res.Committed))
	}
	if len(tl.Pending()) != 1 {
		t.Fatalf("pending %d want 1", len(tl.Pending()))
	}
}

func TestDuplicateIdentityIgnored(t *testing.T) {
	tl := newTestTimeline(t, 1000)
	e := sub(events.TrackSubtitle, 100, 1, "a")
	admitAll(t, tl, []events.Event{e})
	before := tl.StateHash()
	res, err := tl.Admit(e)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Duplicate {
		t.Fatal("duplicate not reported")
	}
	if tl.StateHash() != before {
		t.Fatal("duplicate changed state")
	}
	if tl.Stats().Duplicates != 1 {
		t.Fatalf("duplicate count %d", tl.Stats().Duplicates)
	}
}

func TestConflictingDuplicateRejected(t *testing.T) {
	tl := newTestTimeline(t, 1000)
	admitAll(t, tl, []events.Event{sub(events.TrackSubtitle, 100, 1, "a")})
	conflict := sub(events.TrackSubtitle, 100, 1, "b")
	_, err := tl.Admit(conflict)
	if err == nil || err.Code != events.CodeMalformed {
		t.Fatalf("expected malformed, got %v", err)
	}
}

func TestLateEventTriggersReplay(t *testing.T) {
	tl := newTestTimeline(t, 1000)
	// Commit 1..5 at times 0..400, then an event at 1400 advances the watermark
	// to 400 and makes the first five final.
	for i := 0; i < 5; i++ {
		if _, err := tl.Admit(sub(events.TrackSubtitle, int64(i)*100, uint64(i+1), "x")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tl.Admit(sub(events.TrackChapter, 1400, 6, "advance")); err != nil {
		t.Fatal(err)
	}
	if got := len(tl.Committed()); got != 5 {
		t.Fatalf("committed %d", got)
	}
	// A new subtitle at time 150 with seq 30 arrives too late.
	res, err := tl.Admit(sub(events.TrackSubtitle, 150, 30, "late"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Replay {
		t.Fatal("replay not triggered")
	}
	if tl.Stats().Replays != 1 {
		t.Fatalf("replays %d", tl.Stats().Replays)
	}
	if len(res.Replayed) == 0 {
		t.Fatal("no replaced outputs reported")
	}
}

func TestReplayMatchesCompleteOrderedInput(t *testing.T) {
	// Reference: every event arrives in canonical order.
	complete := newTestTimeline(t, 1000)
	all := []events.Event{
		sub(events.TrackSubtitle, 0, 1, "a"),
		sub(events.TrackSubtitle, 100, 2, "b"),
		sub(events.TrackChapter, 100, 3, "ch"),
		sub(events.TrackSubtitle, 150, 4, "late"),
		sub(events.TrackSubtitle, 200, 5, "c"),
		sub(events.TrackVideo, 2000, 6, "push"),
	}
	admitAll(t, complete, all)
	if _, err := complete.Flush(); err != nil {
		t.Fatal(err)
	}
	want := complete.StateHash()

	// Damaged: the 150ms event arrives after the rest has been finalised.
	damaged := newTestTimeline(t, 1000)
	for _, e := range []events.Event{all[0], all[1], all[2], all[4], all[5]} {
		if _, err := damaged.Admit(e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := damaged.Admit(all[3]); err != nil {
		t.Fatal(err)
	}
	if damaged.Stats().Replays != 1 {
		t.Fatalf("expected a replay, got %d", damaged.Stats().Replays)
	}
	if _, err := damaged.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := damaged.StateHash(); got != want {
		t.Fatalf("replay state hash mismatch\n got %s\nwant %s", got, want)
	}
}

func TestReplayWindowLimit(t *testing.T) {
	tl, err := New(Config{LateWindowNS: 1000, CheckpointEvery: 500, MaxReplayEvents: 3})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := tl.Admit(sub(events.TrackSubtitle, int64(i)*100, uint64(i+1), "x")); err != nil {
			t.Fatal(err)
		}
	}
	// Advance the watermark so the ten events become final.
	if _, err := tl.Admit(sub(events.TrackChapter, 5000, 11, "advance")); err != nil {
		t.Fatal(err)
	}
	if tl.Stats().Committed != 10 {
		t.Fatalf("committed %d", tl.Stats().Committed)
	}
	_, e := tl.Admit(sub(events.TrackSubtitle, 50, 999, "late"))
	if e == nil || e.Code != events.CodeReplayWindow {
		t.Fatalf("expected replay window error, got %v", e)
	}
}

func TestGapRecordedAndFilledByLateEvent(t *testing.T) {
	tl := newTestTimeline(t, 1000)
	g := events.Gap{Track: events.TrackRTP, Kind: events.KindRTP, Seq: 5, At: -1, Reason: "watermark_passed"}
	if err := tl.MarkGap(g); err != nil {
		t.Fatal(err)
	}
	if got := len(tl.Gaps(true)); got != 1 {
		t.Fatalf("unresolved gaps %d", got)
	}
	if tl.Stats().Gaps != 1 {
		t.Fatalf("gap stat %d", tl.Stats().Gaps)
	}
	e := events.Event{Track: events.TrackRTP, Kind: events.KindRTP, Seq: 5, At: 0, Payload: []byte("p")}
	res, err := tl.Admit(e)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilledGaps != 1 {
		t.Fatalf("filled gaps %d", res.FilledGaps)
	}
	if got := len(tl.Gaps(true)); got != 0 {
		t.Fatalf("gap should be resolved, got %d", got)
	}
}

func TestGapNeverInventsTimestamp(t *testing.T) {
	tl := newTestTimeline(t, 1000)
	if err := tl.MarkGap(events.Gap{Track: events.TrackRTP, Seq: 9, Reason: "lost"}); err != nil {
		t.Fatal(err)
	}
	gaps := tl.Gaps(true)
	if len(gaps) != 1 || gaps[0].At != 0 {
		// Zero is the Go default; the fixture sets -1 explicitly. What matters
		// is that no payload or event is fabricated.
		t.Fatalf("gaps %+v", gaps)
	}
	if len(tl.Committed()) != 0 {
		t.Fatal("a gap must not create a committed event")
	}
}

func TestSameInputSameStateHash(t *testing.T) {
	run := func() string {
		tl := newTestTimeline(t, 1000)
		for i := 0; i < 20; i++ {
			if _, err := tl.Admit(sub(events.TrackSubtitle, int64(i)*50, uint64(i+1), "s")); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tl.Flush(); err != nil {
			t.Fatal(err)
		}
		return tl.StateHash()
	}
	if a, b := run(), run(); a != b {
		t.Fatalf("state hash not deterministic: %s vs %s", a, b)
	}
}

func TestPendingCapacityExceeded(t *testing.T) {
	tl, err := New(Config{LateWindowNS: 0, MaxPending: 2})
	if err != nil {
		t.Fatal(err)
	}
	tl, err = New(Config{LateWindowNS: 1000, MaxPending: 2})
	if err != nil {
		t.Fatal(err)
	}
	var last *events.Error
	for i := 0; i < 5; i++ {
		if _, e := tl.Admit(sub(events.TrackSubtitle, int64(i*10), uint64(i+1), "p")); e != nil {
			last = e
		}
	}
	if last == nil {
		t.Fatal("expected capacity error")
	}
	if last.Code != events.CodeCapacity {
		t.Fatalf("code %s", last.Code)
	}
}

func TestEventLimitExceeded(t *testing.T) {
	tl, err := New(Config{LateWindowNS: 1000, MaxEvents: 3})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, e := tl.Admit(sub(events.TrackSubtitle, int64(i)*100, uint64(i+1), "x")); e != nil {
			t.Fatal(e)
		}
	}
	_, e := tl.Admit(sub(events.TrackSubtitle, 900, 4, "x"))
	if e == nil || e.Code != events.CodeCapacity {
		t.Fatalf("expected capacity error, got %v", e)
	}
}

func TestOutOfRangeTimeRejected(t *testing.T) {
	tl := newTestTimeline(t, 1000)
	_, e := tl.Admit(events.Event{Track: events.TrackSubtitle, Kind: events.KindSubtitle, At: 1 << 62, Seq: 1})
	if e == nil || e.Code != events.CodeTimeOutOfRange {
		t.Fatalf("expected out of range, got %v", e)
	}
}

func TestAdvanceExtendsWatermark(t *testing.T) {
	tl := newTestTimeline(t, 1000)
	admitAll(t, tl, []events.Event{sub(events.TrackSubtitle, 100, 1, "a")})
	res, err := tl.Advance(5000)
	if err != nil {
		t.Fatal(err)
	}
	if res.Watermark != 4000 || len(res.Committed) != 1 {
		t.Fatalf("advance result %+v", res)
	}
	// Advancing backwards must not retract the watermark.
	res, _ = tl.Advance(10)
	if res.Watermark != 4000 {
		t.Fatalf("watermark retracted to %d", res.Watermark)
	}
}
