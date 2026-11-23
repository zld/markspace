package transport

import (
	"testing"
	"time"
)

func TestReorderWithinWindow(t *testing.T) {
	r, err := NewReorderer(4)
	if err != nil {
		t.Fatal(err)
	}
	var released []uint64
	push := func(ext uint64) {
		out, _ := r.Push(ext, Packet{ExtSeq: ext})
		for _, p := range out {
			released = append(released, p.ExtSeq)
		}
	}
	// 0, 2, 1, 3 must come out 0,1,2,3 with one reorder event.
	for _, ext := range []uint64{0, 2, 1, 3} {
		push(ext)
	}
	want := []uint64{0, 1, 2, 3}
	if len(released) != len(want) {
		t.Fatalf("released = %v, want %v", released, want)
	}
	for i := range want {
		if released[i] != want[i] {
			t.Fatalf("released = %v, want %v", released, want)
		}
	}
	if r.Stats.Reordered != 1 {
		t.Errorf("reordered = %d, want 1", r.Stats.Reordered)
	}
	if r.Pending() != 0 {
		t.Errorf("pending = %d, want 0", r.Pending())
	}
}

func TestReorderBurstLossExceedsWindow(t *testing.T) {
	r, err := NewReorderer(2)
	if err != nil {
		t.Fatal(err)
	}
	var released []uint64
	push := func(ext uint64) {
		out, _ := r.Push(ext, Packet{ExtSeq: ext})
		for _, p := range out {
			released = append(released, p.ExtSeq)
		}
	}
	// 0..3 arrive, 4 and 5 are lost, 6..9 arrive. With window 2 the loss is
	// declared once seq 8 is observed (8-4 >= 2 after 6,7 release).
	for ext := uint64(0); ext <= 3; ext++ {
		push(ext)
	}
	for ext := uint64(6); ext <= 9; ext++ {
		push(ext)
	}
	tail, gaps := r.Finish()
	for _, p := range tail {
		released = append(released, p.ExtSeq)
	}
	want := []uint64{0, 1, 2, 3, 6, 7, 8, 9}
	if len(released) != len(want) {
		t.Fatalf("released = %v, want %v", released, want)
	}
	for i := range want {
		if released[i] != want[i] {
			t.Fatalf("released = %v, want %v", released, want)
		}
	}
	if len(gaps) != 1 || gaps[0].Start != 4 || gaps[0].End != 5 {
		t.Fatalf("gaps = %+v, want one gap 4..5", gaps)
	}
	if gaps[0].State != GapLost {
		t.Errorf("gap state = %s, want lost", gaps[0].State)
	}
	if r.Stats.Lost != 2 {
		t.Errorf("lost = %d, want 2", r.Stats.Lost)
	}
}

func TestReorderLateAfterLoss(t *testing.T) {
	r, err := NewReorderer(1)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := r.Push(0, Packet{})
	if len(out) != 1 {
		t.Fatalf("seq 0 not released")
	}
	// Seq 1 missing; seq 2 arriving declares 1 lost with window 1.
	if _, _ = r.Push(2, Packet{}); len(r.Gaps()) != 1 {
		t.Fatalf("gaps = %+v", r.Gaps())
	}
	if r.Gaps()[0].State != GapLost {
		t.Fatalf("state = %s", r.Gaps()[0].State)
	}
	// The late packet is rejected as stale and marks the gap late.
	if out, stale := r.Push(1, Packet{}); !stale || len(out) != 0 {
		t.Fatalf("late packet was not rejected: stale=%v out=%d", stale, len(out))
	}
	if got := r.Gaps()[0].State; got != GapLate {
		t.Errorf("state after late arrival = %s, want late_after_loss", got)
	}
	if r.Stats.LateArrived != 1 {
		t.Errorf("late arrived = %d, want 1", r.Stats.LateArrived)
	}
}

func TestReorderFinishClosesStream(t *testing.T) {
	r, err := NewReorderer(16)
	if err != nil {
		t.Fatal(err)
	}
	r.Push(0, Packet{})
	r.Push(2, Packet{})
	r.Push(3, Packet{})
	tail, gaps := r.Finish()
	if len(tail) != 2 { // 2 and 3, held behind the hole at 1
		t.Fatalf("tail = %d packets, want 2", len(tail))
	}
	if len(gaps) != 1 || gaps[0].Start != 1 || gaps[0].End != 1 || gaps[0].State != GapLost {
		t.Fatalf("gaps = %+v", gaps)
	}
	if r.Pending() != 0 {
		t.Errorf("pending after finish = %d", r.Pending())
	}
	// Finish is idempotent.
	if _, again := r.Finish(); len(again) != 1 {
		t.Errorf("second Finish returned %d gaps", len(again))
	}
}

func TestRecorderDeterministicDrill(t *testing.T) {
	drill, err := DefaultRTPDrill(41)
	if err != nil {
		t.Fatal(err)
	}
	st, gaps, ordered, err := drill.Replay(4, 64, 0x4d454449)
	if err != nil {
		t.Fatal(err)
	}
	if st.Duplicates != 3 {
		t.Errorf("duplicates = %d, want 3", st.Duplicates)
	}
	if st.Reordered != 5 {
		t.Errorf("reordered = %d, want 5", st.Reordered)
	}
	if st.Lost != 2 {
		t.Errorf("lost = %d, want 2", st.Lost)
	}
	const anchor = 0x10000
	if len(gaps) != 1 || gaps[0].Start != anchor+10 || gaps[0].End != anchor+11 {
		t.Fatalf("gaps = %+v, want %d..%d", gaps, anchor+10, anchor+11)
	}
	if len(ordered) != 25 {
		t.Errorf("dispatched = %d, want 25", len(ordered))
	}
	for i := 1; i < len(ordered); i++ {
		if ordered[i].ExtSeq <= ordered[i-1].ExtSeq {
			t.Fatalf("output not strictly ordered at %d", i)
		}
	}
	// Replay is reproducible byte for byte.
	st2, gaps2, ordered2, err := drill.Replay(4, 64, 0x4d454449)
	if err != nil {
		t.Fatal(err)
	}
	if st2 != st || len(gaps2) != len(gaps) || gaps2[0] != gaps[0] || len(ordered2) != len(ordered) {
		t.Fatalf("replay not deterministic: %+v vs %+v", st2, st)
	}
}

func TestControllerAdaptsToBandwidth(t *testing.T) {
	ctl, err := NewController(AdaptOptions{
		Window: 3, MinParallel: 1, MaxParallel: 16, MinQueue: 1, MaxQueue: 32,
		TargetBps: 1_000_000, SampleEvery: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ctl.Parallel() != 1 {
		t.Errorf("initial parallel = %d, want 1", ctl.Parallel())
	}
	// Feed a high sustained rate then collapse it, and observe the limits ramp
	// up before the window catches up with the drop.
	var hiSamples []float64
	for i := 0; i < 4; i++ {
		ctl.Observe(timeAt(i, 10), 20_000)
	}
	high := ctl.Parallel()
	_, _, hiMax := ctl.Summary()
	hiSamples = append(hiSamples, hiMax)
	highQueue := ctl.Queue()
	if high <= 1 {
		t.Errorf("parallel after high bandwidth = %d, want > 1", high)
	}
	if highQueue > 32 || highQueue < 1 {
		t.Errorf("queue = %d outside bounds", highQueue)
	}
	// Feed a low rate; limits must fall but never below the floor.
	for i := 4; i < 30; i++ {
		ctl.Observe(timeAt(i, 10), 100)
	}
	low := ctl.Parallel()
	if low >= high {
		t.Errorf("parallel did not fall: high=%d low=%d", high, low)
	}
	if low < 1 {
		t.Errorf("parallel below floor: %d", low)
	}
	min, mean, max := ctl.Summary()
	if !(min <= mean && mean <= max) {
		t.Errorf("summary ordering invalid: min=%f mean=%f max=%f", min, mean, max)
	}
	// The window retains only the latest samples, so the low phase floors the
	// retained summary; the high-phase maximum recorded above proves variation.
	if len(hiSamples) != 1 || hiSamples[0] <= max {
		t.Errorf("bandwidth high watermark %v not above retained max %f", hiSamples, max)
	}
}

func TestAdaptOptionsValidation(t *testing.T) {
	bad := []AdaptOptions{
		{Window: 0, MinParallel: 1, MaxParallel: 2, MinQueue: 1, MaxQueue: 2, TargetBps: 1},
		{Window: 2, MinParallel: 5, MaxParallel: 2, MinQueue: 1, MaxQueue: 2, TargetBps: 1},
		{Window: 2, MinParallel: 1, MaxParallel: 2, MinQueue: 0, MaxQueue: 2, TargetBps: 1},
		{Window: 2, MinParallel: 1, MaxParallel: 2, MinQueue: 1, MaxQueue: 2, TargetBps: 0},
	}
	for i, o := range bad {
		if _, err := NewController(o); err == nil {
			t.Errorf("case %d: invalid options accepted", i)
		}
	}
}

// timeAt returns a monotonic-ish time value for controller tests.
func timeAt(i, stepMs int) time.Time {
	return time.Unix(1700000000, 0).Add(time.Duration(i*stepMs) * time.Millisecond)
}
