package timeline

import (
	"fmt"
	"testing"

	"mediasync/internal/clock"
	"mediasync/internal/issue"
)

func ms(v int) clock.Tick { return clock.FromMilliseconds(int64(v)) }

func ev(ref string, at, end int) Event {
	return Event{Kind: KindCue, Time: ms(at), End: ms(end), Input: "in", Line: at, Offset: -1, Ref: ref}
}

func refs(evs []Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Ref
	}
	return out
}

func TestCanonicalOrderIndependentOfArrival(t *testing.T) {
	base := []Event{ev("a", 0, 10), ev("b", 500, 600), ev("c", 1000, 1100), ev("d", 2000, 2100)}
	orders := [][]int{
		{0, 1, 2, 3},
		{1, 0, 3, 2},
		{0, 1, 3, 2},
		{3, 2, 1, 0},
	}
	var want []string
	for oi, order := range orders {
		tl := New(ms(2000))
		for _, i := range order {
			tl.Push(base[i])
		}
		got := refs(tl.Events())
		if oi == 0 {
			want = got
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("order %v gave %v want %v", order, got, want)
		}
	}
}

func TestInWindowLateRecompute(t *testing.T) {
	// Position 0, 1s, 3s arrive in order; 2s arrives late but inside the
	// 2s window relative to the 3s high-water mark (horizon 1s).
	tl := New(ms(2000))
	for _, e := range []Event{ev("p0", 0, 0), ev("p1", 1000, 1000), ev("p3", 3000, 3000)} {
		tl.Push(e)
	}
	tl.Push(ev("p2", 2000, 2000))
	got := refs(tl.Events())
	want := []string{"p0", "p1", "p2", "p3"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if _, rec, _, _ := tl.Counters(); rec == 0 {
		t.Fatal("no recomputation recorded")
	}
	if len(tl.Replays()) != 1 {
		t.Fatalf("replays=%v", tl.Replays())
	}
	if tl.Replays()[0].Horizon != ms(1000) {
		t.Fatalf("horizon recorded %s want 1s", tl.Replays()[0].Horizon)
	}
}

func TestOutOfWindowUnrecoverable(t *testing.T) {
	tl := New(ms(2000))
	for _, e := range []Event{ev("p0", 0, 0), ev("p1", 1000, 1000), ev("p5", 5000, 5000)} {
		tl.Push(e)
	}
	// 2s is older than horizon 3s: unrecoverable.
	tl.Push(ev("late", 2000, 2000))
	if got := refs(tl.Events()); len(got) != 3 {
		t.Fatalf("late event was folded in: %v", got)
	}
	if _, _, unrec, _ := tl.Counters(); unrec != 1 {
		t.Fatalf("unrec=%d", unrec)
	}
	issues := tl.Issues()
	if len(issues) != 1 || issues[0].Category != issue.LateUnrecoverable {
		t.Fatalf("issues=%v", issues)
	}
	if issues[0].Loc.Ref != "late" || issues[0].Loc.Line != 2000 {
		t.Fatalf("location=%+v", issues[0].Loc)
	}
}

func TestLateAtHorizonIsRecoverable(t *testing.T) {
	tl := New(ms(2000))
	tl.Push(ev("a", 0, 0))
	tl.Push(ev("c", 5000, 5000))
	// horizon = 3000; an event exactly at the horizon is still replayable.
	tl.Push(ev("b", 3000, 3000))
	if got := refs(tl.Events()); fmt.Sprint(got) != fmt.Sprint([]string{"a", "b", "c"}) {
		t.Fatalf("got %v", got)
	}
}

func TestSealedPrefixStable(t *testing.T) {
	tl := New(ms(1000))
	for i := 0; i < 100; i++ {
		tl.Push(ev(fmt.Sprintf("e%03d", i), i*100, i*100+50))
	}
	_, _, _, sealed := tl.Counters()
	if sealed == 0 {
		t.Fatal("nothing sealed")
	}
	before := refs(tl.Events())
	// An in-window late event must not disturb the sealed prefix, and must
	// land at its canonical position in the replayable tail.
	tl.Push(ev("insert", 8950, 8990))
	after := refs(tl.Events())
	if len(after) != len(before)+1 {
		t.Fatalf("event count %d -> %d", len(before), len(after))
	}
	// Events sealed before the arrival (times < horizon 8900) are unchanged.
	if fmt.Sprint(after[:89]) != fmt.Sprint(before[:89]) {
		t.Fatalf("sealed prefix changed:\n%v\n%v", after[:89], before[:89])
	}
	if after[89] != "e089" || after[90] != "insert" || after[91] != "e090" {
		t.Fatalf("insert position wrong: %v", after[87:93])
	}
}

func TestReplayDeterminism(t *testing.T) {
	arrivals := []Event{
		ev("a", 0, 10), ev("c", 2000, 2010), ev("b", 1000, 1010),
		ev("e", 4000, 4010), ev("d", 3000, 3010), ev("f", 6000, 6010),
	}
	run := func() []string {
		tl := New(ms(2500))
		for _, e := range arrivals {
			tl.Push(e)
		}
		return refs(tl.Events())
	}
	first := run()
	for i := 0; i < 5; i++ {
		if got := run(); fmt.Sprint(got) != fmt.Sprint(first) {
			t.Fatalf("run %d differs: %v vs %v", i, got, first)
		}
	}
}

func TestGapOverlapDetection(t *testing.T) {
	tl := New(ms(2000))
	tl.Push(ev("a", 0, 2000))
	tl.Push(ev("b", 1000, 3000))
	gaps := tl.Gaps()
	if len(gaps) != 1 || !gaps[0].Overlaps {
		t.Fatalf("gaps=%+v", gaps)
	}
	if gaps[0].Delta != ms(1000) {
		t.Fatalf("delta=%s", gaps[0].Delta)
	}
}
