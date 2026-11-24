package clock

import "testing"

func TestExtendSeq16(t *testing.T) {
	cases := []struct {
		prev int64
		in   Seq16
		want int64
	}{
		{0, 10, 10},
		{10, 11, 11},
		{0xFFFF, 0, 1 << 16},
		{1 << 16, 0xFFFF, 0xFFFF}, // reordered old packet
		{20, 20, 20},              // duplicate
	}
	for _, c := range cases {
		if got := ExtendSeq16(c.prev, c.in); got != c.want {
			t.Errorf("ExtendSeq16(%d,%d)=%d want %d", c.prev, c.in, got, c.want)
		}
	}
}

func TestExtendTick32Wrap(t *testing.T) {
	prev := Tick(0xFFFFFFF0)
	got := ExtendTick32(prev, 5)
	want := Tick(0x100000005)
	if got != want {
		t.Fatalf("wrap extend = %d want %d", got, want)
	}
	if back := ExtendTick32(want, Tick32(prev)); back != prev {
		t.Fatalf("backward extend = %d want %d", back, prev)
	}
}

func TestSampleConverterRemainder(t *testing.T) {
	c, err := NewSampleConverter(48000)
	if err != nil {
		t.Fatal(err)
	}
	// 48k/90k => 15 ticks per 8 samples; block of 7 samples must retain
	// the fractional remainder across many blocks.
	const blocks = 1_000_000
	var totalSamples int64
	for i := 0; i < blocks; i++ {
		start, end := c.Advance(7)
		if end < start {
			t.Fatalf("converter moved backwards at block %d", i)
		}
		totalSamples += 7
	}
	exact := totalSamples * TicksPerSecond / 48000
	if got := c.Position(); got != Tick(exact) {
		t.Fatalf("accumulated position=%d exact=%d drift=%d ticks", got, exact, int64(got)-exact)
	}
	// Long-stream drift must stay below one source sample period
	// (90000/48000 = 1.875 ticks), i.e. exact rational mapping holds.
	if d := c.Position() - c.SamplesToTick(totalSamples); d != 0 {
		t.Fatalf("drift %d ticks exceeds rational exactness", d)
	}
}

func TestRepairerWraparound(t *testing.T) {
	r, _ := NewRepairer(900, 90)
	base := Tick32(0xFFFFFFF0)
	r.Observe(Seq16(0xFFFF), base)
	e := r.Observe(0, base+20) // wraps both seq and ts
	if e.Reason != ReasonWrap {
		t.Fatalf("reason=%s want wrap", e.Reason)
	}
	if e.Repaired <= r.Entries()[0].Repaired {
		t.Fatalf("repaired did not advance across wrap: %+v", e)
	}
	if !r.IsMonotonic() {
		t.Fatal("timeline not monotonic after wrap")
	}
}

func TestRepairerBackwardsAndDrift(t *testing.T) {
	r, _ := NewRepairer(900, 90)
	r.Observe(0, 1000)
	r.Observe(1, 1900) // nominal 1900, exact
	e := r.Observe(2, 2795) // 5 ticks slow -> drift
	if e.Reason != ReasonDrift || e.Repaired != 2800 {
		t.Fatalf("drift case: reason=%s repaired=%d", e.Reason, e.Repaired)
	}
	b := r.Observe(3, 2000) // clock jumps backwards
	if b.Reason != ReasonBackwards || b.Repaired <= e.Repaired {
		t.Fatalf("backwards case: %+v", b)
	}
	if !r.IsMonotonic() {
		t.Fatal("not monotonic")
	}
}

func TestRepairerJumpKeepsGap(t *testing.T) {
	r, _ := NewRepairer(900, 90)
	r.Observe(0, 0)
	r.Observe(1, 900)
	e := r.Observe(2, 900+9000) // 100 ms gap, far beyond tolerance
	if e.Reason != ReasonJump {
		t.Fatalf("reason=%s want jump", e.Reason)
	}
	if e.Repaired-r.Entries()[1].Repaired != 9000 {
		t.Fatalf("gap not preserved: %d", e.Repaired)
	}
}

func TestRepairerDuplicateAndReorder(t *testing.T) {
	r, _ := NewRepairer(900, 90)
	r.Observe(0, 0)
	r.Observe(1, 900)
	r.Observe(2, 1800)
	dup := r.Observe(1, 900) // duplicate
	if dup.Reason != ReasonDuplicate || dup.Repaired != 900 {
		t.Fatalf("duplicate: %+v", dup)
	}
	// arrival order 0,1,2,4 then late 3
	r.Observe(4, 3600)
	late := r.Observe(3, 2700)
	if late.Reason != ReasonReordered || late.Repaired != 2700 {
		t.Fatalf("reorder: %+v", late)
	}
	if !r.IsMonotonic() {
		t.Fatal("not monotonic")
	}
}

func TestRepairerDeterminism(t *testing.T) {
	build := func() []TimeEntry {
		r, _ := NewRepairer(900, 90)
		seqs := []Seq16{5, 6, 6, 8, 7, 9}
		tss := []Tick32{4500, 5400, 5400, 7200, 6300, 8105}
		for i := range seqs {
			r.Observe(seqs[i], tss[i])
		}
		return r.Entries()
	}
	a, b := build(), build()
	if len(a) != len(b) {
		t.Fatal("length mismatch")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("entry %d differs: %+v vs %+v", i, a[i], b[i])
		}
	}
}
