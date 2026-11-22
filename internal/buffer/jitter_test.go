package buffer

import (
	"testing"

	"example.com/mediaforge/internal/merrors"
)

func mk(seq uint16, tick uint64) Unit {
	return Unit{SSRC: 1, Seq: seq, Extended: tick, Payload: []byte{byte(seq)}}
}

func TestJitterInOrderReleasesOnWindow(t *testing.T) {
	j, err := New(1, Config{WindowTicks: 100, MaxUnits: 64, HighWater: 32})
	if err != nil {
		t.Fatal(err)
	}
	// Nothing can be released until the buffer runs 100 ticks ahead.
	var units []Unit
	for i := uint64(0); i < 10; i++ {
		if err := j.Push(mk(uint16(i), i*10)); err != nil {
			t.Fatal(err)
		}
		u, g := j.Ready()
		if len(g) != 0 {
			t.Fatalf("unexpected gaps: %+v", g)
		}
		units = append(units, u...)
	}
	last := j.units[len(j.units)-1].Extended
	if last != 90 {
		t.Fatalf("edge = %d", last)
	}
	// With edge=90 only ticks <= -10 qualify; nothing does.
	if len(units) != 0 {
		t.Fatalf("released %d too early", len(units))
	}
	if err := j.Push(mk(10, 100)); err != nil {
		t.Fatal(err)
	}
	units, gaps := j.Ready()
	if len(gaps) != 0 {
		t.Fatalf("gaps = %+v", gaps)
	}
	// edge=100, so ticks 0..0 qualify (100-0 >= 100).
	if len(units) != 1 || units[0].Seq != 0 {
		t.Fatalf("units = %+v", units)
	}
}

func TestJitterReportsGapExplicitly(t *testing.T) {
	j, _ := New(1, Config{WindowTicks: 10, MaxUnits: 64, HighWater: 64})
	// Sequence 1..5 with 3 missing.
	for _, seq := range []uint16{1, 2, 4, 5, 6} {
		if err := j.Push(mk(seq, uint64(seq)*10)); err != nil {
			t.Fatal(err)
		}
	}
	units, gaps := j.Flush()
	if len(units) != 5 {
		t.Fatalf("units = %d", len(units))
	}
	if len(gaps) != 1 {
		t.Fatalf("gaps = %+v", gaps)
	}
	g := gaps[0]
	if g.FromSeq != 3 || g.Count != 1 {
		t.Fatalf("gap = %+v", g)
	}
	if !g.HasAfter || g.AfterTick != 20 || !g.HasBefore || g.BeforeTick != 40 {
		t.Fatalf("gap anchors = %+v", g)
	}
	if !g.Final {
		t.Fatal("gap should be final")
	}
	if j.Stats().Gaps != 1 {
		t.Fatalf("stats = %+v", j.Stats())
	}
}

func TestJitterMultiPacketGap(t *testing.T) {
	j, _ := New(1, Config{WindowTicks: 10, MaxUnits: 64, HighWater: 64})
	for _, seq := range []uint16{10, 11, 15} {
		if err := j.Push(mk(seq, uint64(seq))); err != nil {
			t.Fatal(err)
		}
	}
	_, gaps := j.Flush()
	if len(gaps) != 1 || gaps[0].FromSeq != 12 || gaps[0].Count != 3 {
		t.Fatalf("gaps = %+v", gaps)
	}
}

func TestJitterGapBeforeFlushNotReportedEarly(t *testing.T) {
	j, _ := New(1, Config{WindowTicks: 5, MaxUnits: 64, HighWater: 64})
	if err := j.Push(mk(1, 10)); err != nil {
		t.Fatal(err)
	}
	if err := j.Push(mk(3, 30)); err != nil {
		t.Fatal(err)
	}
	// Edge is 30; unit at 10 is 20 behind the window, so it releases, but
	// sequence 2 has not been given up on because 3 is not yet released.
	units, gaps := j.Ready()
	if len(units) != 1 || units[0].Seq != 1 {
		t.Fatalf("units = %+v", units)
	}
	if len(gaps) != 0 {
		t.Fatalf("premature gap: %+v", gaps)
	}
	// Once 3 releases, sequence 2 is definitively gone.
	units, gaps = j.Flush()
	if len(units) != 1 || units[0].Seq != 3 {
		t.Fatalf("units = %+v", units)
	}
	if len(gaps) != 1 || gaps[0].FromSeq != 2 || gaps[0].Count != 1 {
		t.Fatalf("gaps = %+v", gaps)
	}
}

func TestJitterLateAndDuplicateRejected(t *testing.T) {
	j, _ := New(1, Config{WindowTicks: 1, MaxUnits: 64, HighWater: 64})
	if err := j.Push(mk(5, 100)); err != nil {
		t.Fatal(err)
	}
	// A duplicate is caught while both are still buffered.
	if err := j.Push(mk(5, 100)); !merrors.IsCategory(err, merrors.Duplicate) {
		t.Fatalf("dup err = %v", err)
	}
	if _, _ = j.Flush(); true {
		// Released through sequence 5: anything at or below it is late.
		if err := j.Push(mk(5, 100)); !merrors.IsCategory(err, merrors.Late) {
			t.Fatalf("late err = %v", err)
		}
		if err := j.Push(mk(4, 90)); !merrors.IsCategory(err, merrors.Late) {
			t.Fatalf("late err = %v", err)
		}
	}
	if j.Stats().Dropped != 3 {
		t.Fatalf("stats = %+v", j.Stats())
	}
}

func TestJitterReorderedWithinWindowAccepted(t *testing.T) {
	j, _ := New(1, Config{WindowTicks: 1000, MaxUnits: 64, HighWater: 64})
	for _, seq := range []uint16{1, 3, 2, 4} {
		if err := j.Push(mk(seq, uint64(seq)*100)); err != nil {
			t.Fatalf("seq %d: %v", seq, err)
		}
	}
	units, gaps := j.Flush()
	if len(gaps) != 0 {
		t.Fatalf("gaps = %+v", gaps)
	}
	if len(units) != 4 {
		t.Fatalf("units = %d", len(units))
	}
	for i, want := range []uint16{1, 2, 3, 4} {
		if units[i].Seq != want {
			t.Fatalf("units[%d] = %d, want %d", i, units[i].Seq, want)
		}
	}
}

func TestJitterHighWaterForcesRelease(t *testing.T) {
	j, _ := New(1, Config{WindowTicks: 1 << 20, MaxUnits: 8, HighWater: 4})
	for seq := uint16(1); seq <= 4; seq++ {
		if err := j.Push(mk(seq, uint64(seq))); err != nil {
			t.Fatal(err)
		}
	}
	// Still below high water: nothing releases despite the huge window.
	if u, _ := j.Ready(); len(u) != 0 {
		t.Fatalf("released early: %+v", u)
	}
	if err := j.Push(mk(5, 5)); err != nil {
		t.Fatal(err)
	}
	units, _ := j.Ready()
	if len(units) != 1 {
		t.Fatalf("high water released %d, want 1", len(units))
	}
}

func TestJitterBounds(t *testing.T) {
	if _, err := New(1, Config{WindowTicks: 0, MaxUnits: 8, HighWater: 4}); err == nil {
		t.Fatal("expected window error")
	}
	if _, err := New(1, Config{WindowTicks: 1, MaxUnits: 0, HighWater: 4}); err == nil {
		t.Fatal("expected max units error")
	}
	if _, err := New(1, Config{WindowTicks: 1, MaxUnits: 8, HighWater: 9}); err == nil {
		t.Fatal("expected high water error")
	}

	j, _ := New(1, Config{WindowTicks: 1, MaxUnits: 2, HighWater: 2})
	if err := j.Push(mk(1, 1)); err != nil {
		t.Fatal(err)
	}
	if err := j.Push(mk(2, 2)); err != nil {
		t.Fatal(err)
	}
	if err := j.Push(mk(3, 3)); !merrors.IsCategory(err, merrors.Overflow) {
		t.Fatalf("overflow err = %v", err)
	}
	if err := j.Push(Unit{SSRC: 2, Seq: 1}); !merrors.IsCategory(err, merrors.Invalid) {
		t.Fatalf("ssrc err = %v", err)
	}
}

func TestJitterSameTimestampMultipleUnits(t *testing.T) {
	// Several RTP packets may share one frame timestamp; both must be kept.
	j, _ := New(1, Config{WindowTicks: 1, MaxUnits: 16, HighWater: 16})
	u1 := mk(1, 500)
	u1.Marker = false
	u2 := mk(2, 500)
	u2.Marker = true
	if err := j.Push(u1); err != nil {
		t.Fatal(err)
	}
	if err := j.Push(u2); err != nil {
		t.Fatal(err)
	}
	units, gaps := j.Flush()
	if len(units) != 2 || len(gaps) != 0 {
		t.Fatalf("units=%d gaps=%+v", len(units), gaps)
	}
	if !units[1].Marker {
		t.Fatal("marker lost")
	}
}

func TestJitterDeterministic(t *testing.T) {
	run := func() ([]uint16, []Gap) {
		j, _ := New(1, Config{WindowTicks: 4, MaxUnits: 64, HighWater: 64})
		for _, seq := range []uint16{3, 1, 2, 6, 5, 8} {
			if err := j.Push(mk(seq, uint64(seq)*10)); err != nil {
				t.Fatal(err)
			}
			_, _ = j.Ready()
		}
		u, g := j.Flush()
		var order []uint16
		for _, x := range u {
			order = append(order, x.Seq)
		}
		return order, g
	}
	a, ga := run()
	b, gb := run()
	if len(a) != len(b) || len(ga) != len(gb) {
		t.Fatalf("nondeterministic lengths")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("nondeterministic order at %d", i)
		}
	}
	for i := range ga {
		if ga[i] != gb[i] {
			t.Fatalf("nondeterministic gap at %d: %+v vs %+v", i, ga[i], gb[i])
		}
	}
}

func TestJitterFrontier(t *testing.T) {
	j, _ := New(1, DefaultConfig())
	if _, ok := j.Frontier(); ok {
		t.Fatal("frontier before any release")
	}
	if err := j.Push(mk(1, 77)); err != nil {
		t.Fatal(err)
	}
	if _, _ = j.Flush(); true {
		if got, ok := j.Frontier(); !ok || got != 77 {
			t.Fatalf("frontier = %d %v", got, ok)
		}
	}
}
