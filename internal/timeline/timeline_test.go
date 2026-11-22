package timeline

import (
	"testing"

	"mediapipeline/internal/media"
)

func ms(v int64) media.Nano { return media.Nano(v) * media.Nano(1_000_000) }

func testConfig(t *testing.T) Config {
	t.Helper()
	cfg := Config{
		Segment:             ms(1000),
		AllowedLateness:     ms(100),
		MaxReplayWindow:     ms(20000),
		MaxEventsPerSegment: 1000,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
}

func feed(t *testing.T, tl *Timeline, evs []Event) {
	t.Helper()
	for _, ev := range evs {
		if err := tl.Push(ev); err != nil {
			t.Fatalf("Push(%+v): %v", ev, err)
		}
	}
}

func TestWatermarkMonotonicAndLagged(t *testing.T) {
	w, err := NewWatermark(ms(100))
	if err != nil {
		t.Fatal(err)
	}
	late, v := w.Observe(ms(1000))
	if late || v != ms(900) {
		t.Fatalf("first observe: late=%v value=%d", late, v)
	}
	late, v = w.Observe(ms(500))
	if !late || v != ms(900) {
		t.Fatalf("late observe: late=%v value=%d (watermark must not regress)", late, v)
	}
	late, v = w.Observe(ms(2000))
	if late || v != ms(1900) {
		t.Fatalf("advance observe: late=%v value=%d", late, v)
	}
	if w.Advances() != 2 {
		t.Fatalf("advances = %d", w.Advances())
	}
}

func TestWatermarkNeverNegative(t *testing.T) {
	w, _ := NewWatermark(ms(500))
	_, v := w.Observe(ms(100))
	if v != 0 {
		t.Fatalf("watermark = %d, want clamped to 0", v)
	}
}

func TestTimelineSealsInOrder(t *testing.T) {
	tl, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	// Arrive out of order within the reorder window.
	feed(t, tl, []Event{
		{Time: ms(1500), Track: 1, Seq: 15},
		{Time: ms(100), Track: 1, Seq: 1},
		{Time: ms(1200), Track: 1, Seq: 12},
		{Time: ms(2000), Track: 2, Seq: 20},
		{Time: ms(2400), Track: 1, Seq: 24},
	})
	// The newest event (2400ms) minus 100ms lateness seals [0,2000).
	if got := len(tl.Segments()); got != 2 {
		t.Fatalf("sealed %d segments, want 2", got)
	}
	feed(t, tl, []Event{{Time: ms(4000), Track: 1, Seq: 40}})
	if err := tl.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	segs := tl.Segments()
	if len(segs) != 5 {
		t.Fatalf("segments = %d, want 5", len(segs))
	}
	var prevEnd media.Nano
	for i, s := range segs {
		if s.Index != i {
			t.Errorf("segment %d has Index %d", i, s.Index)
		}
		if s.Start < prevEnd {
			t.Errorf("segment %d starts before previous end", i)
		}
		prevEnd = s.End
		for j := 1; j < len(s.Events); j++ {
			if s.Events[j].Less(s.Events[j-1]) {
				t.Errorf("segment %d events not sorted", i)
			}
		}
	}
	// First segment holds the 100ms event.
	if len(segs[0].Events) != 1 || segs[0].Events[0].Seq != 1 {
		t.Errorf("segment 0 events = %+v", segs[0].Events)
	}
}

func TestReplayFromAnyOrderIsIdentical(t *testing.T) {
	events := []Event{
		{Time: ms(10), Track: 1, Seq: 1},
		{Time: ms(250), Track: 2, Seq: 7},
		{Time: ms(250), Track: 1, Seq: 3},
		{Time: ms(640), Track: 1, Seq: 4},
		{Time: ms(1100), Track: 1, Seq: 9},
		{Time: ms(1950), Track: 2, Seq: 2},
	}
	rp, err := NewReplay(ms(1000), ms(10000))
	if err != nil {
		t.Fatal(err)
	}
	var baseline string
	// All 6 rotations and the full reverse must agree.
	orders := [][]int{
		{0, 1, 2, 3, 4, 5},
		{5, 4, 3, 2, 1, 0},
		{3, 0, 5, 1, 4, 2},
		{2, 4, 0, 5, 1, 3},
	}
	for _, order := range orders {
		perm := make([]Event, len(events))
		for i, o := range order {
			perm[i] = events[o]
		}
		tl, err := New(testConfig(t))
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range perm {
			if err := tl.Push(ev); err != nil {
				t.Fatalf("push %+v: %v", ev, err)
			}
		}
		if err := tl.Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
		_, crossHash, err := rp.CrossCheck(tl)
		if err != nil {
			t.Fatalf("crosscheck: %v", err)
		}
		ownHash := HashSegments(tl.Segments())
		if crossHash != ownHash {
			t.Fatalf("order %v: replay hash %s != timeline hash %s", order, crossHash, ownHash)
		}
		if baseline == "" {
			baseline = ownHash
		} else if ownHash != baseline {
			t.Fatalf("order %v changed output hash: %s != %s", order, ownHash, baseline)
		}
	}
}

func TestLateEventTriggersReplayOfSealedInterval(t *testing.T) {
	tl, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < 5; i++ {
		feed(t, tl, []Event{{Time: ms(i*1000 + 10), Track: 1, Seq: uint64(i)}})
	}
	feed(t, tl, []Event{{Time: ms(9000), Track: 1, Seq: 100}})
	before := len(tl.Segments())
	if before < 3 {
		t.Fatalf("expected segments sealed by watermark, got %d", before)
	}
	// Insert a late event into the first sealed segment.
	lateEv := Event{Time: ms(500), Track: 2, Seq: 42}
	if err := tl.Push(lateEv); err != nil {
		t.Fatalf("late push: %v", err)
	}
	if err := tl.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	recs := tl.LateRecords()
	if len(recs) != 1 {
		t.Fatalf("late records = %d", len(recs))
	}
	if recs[0].Interval != (Interval{Start: 0, End: ms(1000)}) {
		t.Errorf("replay interval = %+v", recs[0].Interval)
	}
	seg := tl.Segments()[0]
	if seg.Replays != 1 {
		t.Errorf("segment replays = %d", seg.Replays)
	}
	found := false
	for _, ev := range seg.Events {
		if ev == lateEv {
			found = true
		}
	}
	if !found {
		t.Error("late event missing from recomputed segment")
	}
	// Determinism: a fresh timeline fed the same events in canonical order
	// must produce an identical hash.
	all := []Event{}
	for _, s := range tl.Segments() {
		all = append(all, s.Events...)
	}
	ref, _ := New(testConfig(t))
	for _, ev := range all {
		if err := ref.Push(ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := ref.Flush(); err != nil {
		t.Fatal(err)
	}
	if HashSegments(ref.Segments()) != HashSegments(tl.Segments()) {
		t.Fatal("late-event timeline differs from canonical-order timeline")
	}
}

func TestLateEventBeyondReplayWindowFails(t *testing.T) {
	cfg := testConfig(t)
	cfg.MaxReplayWindow = ms(2000)
	tl, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	feed(t, tl, []Event{{Time: 0, Track: 1, Seq: 1}})
	feed(t, tl, []Event{{Time: ms(6000), Track: 1, Seq: 2}})
	err = tl.Push(Event{Time: ms(500), Track: 1, Seq: 3})
	if err == nil {
		t.Fatal("expected replay limit error")
	}
	if media.CategoryOf(err) != media.ReplayLimitExceeded {
		t.Fatalf("category = %v", media.CategoryOf(err))
	}
	if e := media.AsError(err); !e.HasTrack || !e.HasOff {
		t.Fatalf("error missing location: %v", err)
	}
}

func TestDuplicateEventsCountedNotSealed(t *testing.T) {
	tl, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	ev := Event{Time: ms(10), Track: 1, Seq: 1}
	feed(t, tl, []Event{ev, ev, ev})
	if err := tl.Flush(); err != nil {
		t.Fatal(err)
	}
	if tl.EventCount() != 1 {
		t.Fatalf("event count = %d, want 1", tl.EventCount())
	}
	if tl.Counts()[media.DuplicatePacket] != 2 {
		t.Fatalf("duplicate count = %d", tl.Counts()[media.DuplicatePacket])
	}
}

func TestSegmentCapacityLimit(t *testing.T) {
	cfg := testConfig(t)
	cfg.MaxEventsPerSegment = 2
	tl, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	feed(t, tl, []Event{
		{Time: ms(1), Track: 1, Seq: 1},
		{Time: ms(2), Track: 1, Seq: 2},
	})
	err = tl.Push(Event{Time: ms(3), Track: 1, Seq: 3})
	if err == nil || media.CategoryOf(err) != media.ReplayLimitExceeded {
		t.Fatalf("expected capacity error, got %v", err)
	}
}

func TestFlushEmitsEmptyGapSegment(t *testing.T) {
	tl, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	// Segment 1 empty, segment 2 populated: sealing must not silently skip it.
	feed(t, tl, []Event{
		{Time: ms(10), Track: 1, Seq: 1},
		{Time: ms(2100), Track: 1, Seq: 2},
	})
	if err := tl.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	segs := tl.Segments()
	if len(segs) != 3 {
		t.Fatalf("segments = %d, want 3 (including empty gap)", len(segs))
	}
	if len(segs[1].Events) != 0 {
		t.Errorf("gap segment has %d events", len(segs[1].Events))
	}
	if tl.Counts()[media.UnrecoverableGap] != 1 {
		t.Errorf("gap count = %d", tl.Counts()[media.UnrecoverableGap])
	}
}

func TestRecomputeRejectsOversizedWindow(t *testing.T) {
	rp, err := NewReplay(ms(1000), ms(2000))
	if err != nil {
		t.Fatal(err)
	}
	_, err = rp.Recompute(nil, 0, ms(5000))
	if err == nil || media.CategoryOf(err) != media.ReplayLimitExceeded {
		t.Fatalf("expected replay limit error, got %v", err)
	}
}

func TestReplayMarksOutOfWindowEvents(t *testing.T) {
	rp, _ := NewReplay(ms(1000), ms(10000))
	res, err := rp.Recompute([]Event{{Time: ms(10)}, {Time: ms(5000)}}, 0, ms(1000))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("expected truncated flag for out-of-window event")
	}
}

func TestHashChangesWhenPayloadChanges(t *testing.T) {
	a := []Segment{{Index: 0, Start: 0, End: ms(1000), Events: []Event{{Time: 1, Track: 1, Seq: 1, Payload: "a"}}}}
	b := []Segment{{Index: 0, Start: 0, End: ms(1000), Events: []Event{{Time: 1, Track: 1, Seq: 1, Payload: "b"}}}}
	if HashSegments(a) == HashSegments(b) {
		t.Fatal("hash did not change with payload")
	}
	empty1 := HashSegments(nil)
	empty2 := HashSegments([]Segment{})
	if empty1 != empty2 {
		t.Fatal("empty hash must be stable")
	}
}

func TestWatermarkSealsGapsWithoutStalling(t *testing.T) {
	cfg := testConfig(t)
	tl, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Segment 1 is never populated; a far-ahead event must still advance the
	// watermark and seal [0,1000) and [1000,2000) rather than pinning memory.
	feed(t, tl, []Event{
		{Time: ms(10), Track: 1, Seq: 1},
		{Time: ms(9000), Track: 1, Seq: 2},
	})
	segs := tl.Segments()
	if len(segs) < 3 {
		t.Fatalf("sealed %d segments, expected the watermark to pass the gap", len(segs))
	}
	if !segs[1].IsGap || len(segs[1].Events) != 0 {
		t.Fatalf("segment 1 = %+v, want an empty gap segment", segs[1])
	}
	if tl.Counts()[media.UnrecoverableGap] == 0 {
		t.Error("gap was not counted")
	}
	// Every sealed interval must still tile the timeline exactly.
	for i := 1; i < len(segs); i++ {
		if segs[i].Start != segs[i-1].End {
			t.Fatalf("sealed segments are not contiguous at %d", i)
		}
	}
}

func TestLateEventFillsSealedGapAndRetiresIt(t *testing.T) {
	cfg := testConfig(t)
	tl, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	feed(t, tl, []Event{
		{Time: ms(10), Track: 1, Seq: 1},
		{Time: ms(9000), Track: 1, Seq: 2},
	})
	before := tl.Counts()[media.UnrecoverableGap]
	if before == 0 {
		t.Fatal("setup did not produce a gap")
	}
	// Deliver an event into the gap interval; it is no longer missing.
	if err := tl.Push(Event{Time: ms(1500), Track: 3, Seq: 7}); err != nil {
		t.Fatalf("filling a gap: %v", err)
	}
	after := tl.Counts()[media.UnrecoverableGap]
	if after >= before {
		t.Fatalf("gap count did not fall: %d -> %d", before, after)
	}
	seg := tl.Segments()[1]
	if seg.IsGap {
		t.Error("filled segment is still flagged as a gap")
	}
	if len(seg.Events) != 1 || seg.Events[0].Seq != 7 {
		t.Fatalf("segment events = %+v", seg.Events)
	}
	if seg.Replays != 1 {
		t.Errorf("replays = %d", seg.Replays)
	}
}
