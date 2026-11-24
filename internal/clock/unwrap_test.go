package clock

import (
	"testing"
)

func obs(pid uint16, kind Kind, raw uint64, offset int64, lane Lane) Obs {
	return Obs{PID: pid, Kind: kind, Raw: raw, Offset: offset, Packet: offset / 188, Lane: lane}
}

func TestPCRUnwrapAcross33BitWrap(t *testing.T) {
	u := NewUnwrapper(DefaultConfig())
	// Ten PCRs spaced 3000 ticks (90 kHz) apart, starting 5 seconds before
	// the 33-bit boundary. All 27 MHz, so multiply by 300.
	start := uint64(1<<33) - 5*90000
	for i := 0; i < 10; i++ {
		raw := (start + uint64(i)*3000) & (1<<33 - 1)
		v, ok := u.Observe(obs(0x101, PCR, raw*300, int64(i*188), LaneVideo))
		if !ok {
			t.Fatalf("step %d rejected: %v", i, u.Errors())
		}
		// The shared timeline is anchored at the first PCR, so every value
		// is an offset from that anchor.
		want := int64(i) * 3000
		if v != want {
			t.Errorf("step %d ticks=%d want %d", i, v, want)
		}
	}
	if err := u.Finish().Err(); err != nil {
		t.Fatalf("repaired clock reported an error: %v", err)
	}
}

func TestPTSUnwrapAcrossWrapPreservesOrder(t *testing.T) {
	u := NewUnwrapper(DefaultConfig())
	start := uint64(1<<33) - 2*90000
	for i := 0; i < 20; i++ {
		raw := (start + uint64(i)*9000) & (1<<33 - 1) // 100 ms cadence
		v, ok := u.Observe(obs(0x101, PTS, raw, int64(i*188), LaneVideo))
		if !ok {
			t.Fatalf("PTS %d rejected: %v", i, u.Errors())
		}
		if v != int64(i)*9000 {
			t.Errorf("PTS %d = %d, want %d", i, v, i*9000)
		}
	}
}

func TestNonMonotonicTimestampBlocks(t *testing.T) {
	u := NewUnwrapper(DefaultConfig())
	if _, ok := u.Observe(obs(0x101, PTS, 900000, 188*10, LaneVideo)); !ok {
		t.Fatal("first PTS rejected")
	}
	// Jump backward by 3 seconds, beyond the 1-second reorder window.
	if _, ok := u.Observe(obs(0x101, PTS, 900000-3*90000, 188*11, LaneVideo)); ok {
		t.Fatal("large backward jump accepted")
	}
	blocked := u.Errors().Blocking()
	if len(blocked) == 0 {
		t.Fatal("no blocking diagnostic for a backward jump")
	}
	if blocked[0].PID != 0x101 || blocked[0].Offset != 188*11 {
		t.Errorf("blocking error location %+v", blocked[0])
	}
	if _, isBlocked := IsBlocked(u.Errors().Err()); !isBlocked {
		t.Fatal("blocked error did not classify as blocked")
	}
}

func TestDiscontinuityFlagPermitsReset(t *testing.T) {
	u := NewUnwrapper(DefaultConfig())
	o := obs(0x101, PTS, 1000*90000, 0, LaneVideo)
	if _, ok := u.Observe(o); !ok {
		t.Fatal("initial PTS rejected")
	}
	reset := obs(0x101, PTS, 5*90000, 188, LaneVideo)
	reset.Discontinuity = true
	if _, ok := u.Observe(reset); !ok {
		t.Fatalf("flagged discontinuity rejected: %v", u.Errors())
	}
	if len(u.Errors().Blocking()) != 0 {
		t.Fatalf("flagged discontinuity produced blocking errors: %v", u.Errors())
	}
}

func TestAVRelativeOrderEnforced(t *testing.T) {
	u := NewUnwrapper(DefaultConfig())
	v, _ := u.Observe(obs(0x101, PTS, 900000, 0, LaneVideo))
	if v < 0 {
		t.Fatal("video PTS rejected")
	}
	// Audio first sample arrives 10 seconds after video: outside the limit.
	_, ok := u.Observe(obs(0x102, PTS, 900000+10*90000, 188, LaneAudio))
	if !ok {
		t.Fatalf("audio sample rejected: %v", u.Errors())
	}
	errs := u.Finish()
	found := false
	for _, e := range errs {
		if e.Kind == KindAVOrder && e.Blocking {
			found = true
		}
	}
	if !found {
		t.Fatalf("relative A/V order violation not reported: %v", errs)
	}
}

func TestPCRJumpWithinLimitAccepted(t *testing.T) {
	u := NewUnwrapper(DefaultConfig())
	for i := 0; i < 5; i++ {
		raw := uint64(1000+i*3000) * 300
		if _, ok := u.Observe(obs(0x101, PCR, raw, int64(i*188), LaneVideo)); !ok {
			t.Fatalf("in-range PCR %d rejected: %v", i, u.Errors())
		}
	}
}

func TestPCRExcessiveJumpBlocks(t *testing.T) {
	u := NewUnwrapper(DefaultConfig())
	if _, ok := u.Observe(obs(0x101, PCR, 900000*300, 0, LaneVideo)); !ok {
		t.Fatal("first PCR rejected")
	}
	// 10 seconds forward (in base ticks) exceeds the 5-second default limit.
	if _, ok := u.Observe(obs(0x101, PCR, (900000+10*90000)*300, 188, LaneVideo)); ok {
		t.Fatal("10-second PCR jump accepted")
	}
	if len(u.Errors().Blocking()) == 0 {
		t.Fatal("no blocking diagnostic for an excessive jump")
	}
}

func TestForwardVersionRule(t *testing.T) {
	if !ForwardVersion(31, 0) {
		t.Fatal("31 -> 0 must be forward")
	}
	if ForwardVersion(0, 31) {
		t.Fatal("0 -> 31 must be stale, not forward")
	}
	if ForwardVersion(5, 5) {
		t.Fatal("equal versions are duplicates, not forward")
	}
	if !ForwardVersion(0, 1) {
		t.Fatal("0 -> 1 must be forward")
	}
}

func TestUnwrapIsDeterministic(t *testing.T) {
	run := func() []int64 {
		u := NewUnwrapper(DefaultConfig())
		var out []int64
		start := uint64(1<<33) - 400000
		for i := 0; i < 50; i++ {
			raw := (start + uint64(i)*2000) & (1<<33 - 1)
			v, ok := u.Observe(obs(0x101, PTS, raw, int64(i*188), LaneVideo))
			if !ok {
				panic("rejected")
			}
			out = append(out, v)
		}
		return out
	}
	a, b := run(), run()
	if len(a) != len(b) {
		t.Fatal("length mismatch")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("step %d: %d != %d", i, a[i], b[i])
		}
	}
}
