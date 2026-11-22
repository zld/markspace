package index

import (
	"testing"

	"example.com/mediaforge/internal/fixture"
	"example.com/mediaforge/internal/merrors"
)

// buildVFR builds an index whose durations match the shared fixture's irregular
// video pattern.
func buildVFR(t *testing.T, n int) *Index {
	t.Helper()
	durs := fixture.VideoDurations(n)
	keys := fixture.Keyframes(n)
	ds := make([]uint64, n)
	for i, d := range durs {
		ds[i] = uint64(d)
	}
	idx, err := Build(90000, ds, keys)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestBuildVFRCumulativeTimes(t *testing.T) {
	durs := []uint64{3000, 1500, 4500, 3000}
	keys := []bool{true, false, true, false}
	idx, err := Build(90000, durs, keys)
	if err != nil {
		t.Fatal(err)
	}
	if idx.Empty() {
		t.Fatal("index reported empty")
	}
	if idx.Samples() != 4 {
		t.Fatalf("samples = %d", idx.Samples())
	}
	if idx.EndTick() != 12000 {
		t.Fatalf("end = %d, want 12000", idx.EndTick())
	}
	es := idx.Entries()
	if len(es) != 2 {
		t.Fatalf("entries = %+v", es)
	}
	if es[0] != (Entry{SampleIndex: 0, Tick: 0}) {
		t.Fatalf("entry 0 = %+v", es[0])
	}
	if es[1] != (Entry{SampleIndex: 2, Tick: 4500}) {
		t.Fatalf("entry 1 = %+v", es[1])
	}
}

func TestLookupReturnsLatestAtOrBefore(t *testing.T) {
	idx := buildVFR(t, 64)
	for target := uint64(0); target < idx.EndTick(); target += 137 {
		e, err := idx.Lookup(target)
		if err != nil {
			t.Fatalf("target %d: %v", target, err)
		}
		if e.Tick > target && target >= idx.Entries()[0].Tick {
			t.Fatalf("target %d returned later keyframe at %d", target, e.Tick)
		}
		// Every keyframe strictly between the returned one and the target
		// would contradict "latest at or before".
		for _, other := range idx.Entries() {
			if other.Tick > e.Tick && other.Tick <= target {
				t.Fatalf("target %d: missed later keyframe at %d (got %d)", target, other.Tick, e.Tick)
			}
		}
	}
}

func TestLookupExactKeyframe(t *testing.T) {
	idx := buildVFR(t, 32)
	for _, want := range idx.Entries() {
		got, err := idx.Lookup(want.Tick)
		if err != nil {
			t.Fatalf("tick %d: %v", want.Tick, err)
		}
		if got != want {
			t.Fatalf("tick %d: got %+v, want %+v", want.Tick, got, want)
		}
	}
}

func TestLookupBeforeFirstKeyframeClamps(t *testing.T) {
	durs := []uint64{1000, 1000, 1000}
	keys := []bool{false, true, false}
	idx, err := Build(90000, durs, keys)
	if err != nil {
		t.Fatal(err)
	}
	// Tick 0 has no keyframe; the first keyframe is at 1000.
	e, err := idx.Lookup(0)
	if !merrors.IsCategory(err, merrors.OutOfRange) {
		t.Fatalf("err = %v", err)
	}
	if e.SampleIndex != 1 || e.Tick != 1000 {
		t.Fatalf("clamped entry = %+v", e)
	}
}

func TestLookupOutOfRangeAndEmpty(t *testing.T) {
	idx := buildVFR(t, 16)
	if _, err := idx.Lookup(idx.EndTick()); !merrors.IsCategory(err, merrors.OutOfRange) {
		t.Fatalf("end err = %v", err)
	}
	if _, err := idx.Lookup(idx.EndTick() + 1000); !merrors.IsCategory(err, merrors.OutOfRange) {
		t.Fatalf("past end err = %v", err)
	}

	empty, err := Build(90000, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !empty.Empty() {
		t.Fatal("empty index not reported")
	}
	if _, err := empty.Lookup(0); !merrors.IsCategory(err, merrors.Empty) {
		t.Fatalf("empty err = %v", err)
	}

	// Samples but no keyframes: also an empty index for lookup purposes.
	noKeys, err := Build(90000, []uint64{1000, 1000}, []bool{false, false})
	if err != nil {
		t.Fatal(err)
	}
	if noKeys.HasKeyframes() {
		t.Fatal("unexpected keyframes")
	}
	if _, err := noKeys.Lookup(500); !merrors.IsCategory(err, merrors.Empty) {
		t.Fatalf("no-key err = %v", err)
	}
}

func TestBuildValidation(t *testing.T) {
	if _, err := Build(0, []uint64{1}, []bool{true}); !merrors.IsCategory(err, merrors.Invalid) {
		t.Fatalf("timescale err = %v", err)
	}
	if _, err := Build(90000, []uint64{1, 2}, []bool{true}); !merrors.IsCategory(err, merrors.Invalid) {
		t.Fatalf("length err = %v", err)
	}
	if _, err := Build(90000, []uint64{1000, 0}, []bool{true, false}); !merrors.IsCategory(err, merrors.Invalid) {
		t.Fatalf("zero duration err = %v", err)
	}
	big := make([]uint64, MaxSamples+1)
	flags := make([]bool, MaxSamples+1)
	if _, err := Build(90000, big, flags); !merrors.IsCategory(err, merrors.Bounds) {
		t.Fatalf("bounds err = %v", err)
	}
	// Overflow of the cumulative timeline.
	if _, err := Build(90000, []uint64{^uint64(0), 1, 1}, []bool{true, false, false}); err == nil {
		t.Fatal("expected overflow error")
	}
}

func TestKeyframesInRange(t *testing.T) {
	idx := buildVFR(t, 64)
	all := idx.Entries()
	got := idx.KeyframesInRange(all[1].Tick, all[4].Tick)
	if len(got) != 3 {
		t.Fatalf("range = %+v", got)
	}
	if got[0] != all[1] || got[2] != all[3] {
		t.Fatalf("range = %+v", got)
	}
	if idx.KeyframesInRange(10, 10) != nil {
		t.Fatal("inverted range returned entries")
	}
	// [0,1) contains only the keyframe at tick 0.
	first := idx.KeyframesInRange(0, 1)
	if len(first) != 1 || first[0].Tick != 0 {
		t.Fatalf("range at origin = %+v", first)
	}
}

func TestCutPointsDeterministic(t *testing.T) {
	idx := buildVFR(t, 96)
	a, err := idx.CutPoints(12)
	if err != nil {
		t.Fatal(err)
	}
	b, err := idx.CutPoints(12)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatalf("cut counts differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("nondeterministic cut %d: %+v vs %+v", i, a[i], b[i])
		}
	}
	if len(a) == 0 {
		t.Fatal("no cut points")
	}
	// Cut points must be increasing in both sample and time.
	for i := 1; i < len(a); i++ {
		if a[i].SampleIndex <= a[i-1].SampleIndex || a[i].Tick <= a[i-1].Tick {
			t.Fatalf("cuts not increasing at %d: %+v", i, a)
		}
	}
}

func TestCutPointsValidation(t *testing.T) {
	idx := buildVFR(t, 16)
	if _, err := idx.CutPoints(0); !merrors.IsCategory(err, merrors.Invalid) {
		t.Fatalf("zero count err = %v", err)
	}
	if _, err := idx.CutPoints(1000); !merrors.IsCategory(err, merrors.OutOfRange) {
		t.Fatalf("too many err = %v", err)
	}
	empty, _ := Build(90000, nil, nil)
	if _, err := empty.CutPoints(2); !merrors.IsCategory(err, merrors.Empty) {
		t.Fatalf("empty err = %v", err)
	}
}

func TestCutPointsAtRegularKeyframes(t *testing.T) {
	// 100 samples of 1000 ticks; every 10th is a keyframe.
	durs := make([]uint64, 100)
	keys := make([]bool, 100)
	for i := range durs {
		durs[i] = 1000
		keys[i] = i%10 == 0
	}
	idx, err := Build(90000, durs, keys)
	if err != nil {
		t.Fatal(err)
	}
	cuts, err := idx.CutPoints(5)
	if err != nil {
		t.Fatal(err)
	}
	want := []uint64{0, 20000, 40000, 60000, 80000}
	if len(cuts) != len(want) {
		t.Fatalf("cuts = %+v", cuts)
	}
	for i := range want {
		if cuts[i].Tick != want[i] {
			t.Fatalf("cut %d = %d, want %d", i, cuts[i].Tick, want[i])
		}
	}
}

func TestIndexImmutability(t *testing.T) {
	idx := buildVFR(t, 16)
	es := idx.Entries()
	if len(es) > 0 {
		es[0].Tick = 999999
	}
	if idx.Entries()[0].Tick == 999999 {
		t.Fatal("Entries exposes internal storage")
	}
}
