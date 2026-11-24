package media

import (
	"math"
	"testing"
)

func TestUnwrapForwardWrap(t *testing.T) {
	u := NewUnwrapper(32)
	const max = uint64(1)<<32 - 1
	seq := []uint64{max - 3, max - 2, max - 1, max, 0, 1, 2}
	var got []uint64
	for _, r := range seq {
		got = append(got, u.Unwrap(r))
	}
	for i := 1; i < len(got); i++ {
		if got[i] != got[i-1]+1 {
			t.Fatalf("unwrap not continuous at %d: %v", i, got)
		}
	}
}

func TestUnwrapReorderedNearWrap(t *testing.T) {
	u := NewUnwrapper(32)
	const max = uint64(1)<<32 - 1
	// Packet "max" arrives one slot late, after the wrap to 0.
	in := []uint64{max - 1, 0, max, 1}
	want := []uint64{max - 1, max + 1, max, max + 2}
	for i, r := range in {
		if got := u.Unwrap(r); got != want[i] {
			t.Fatalf("Unwrap(%d) = %d, want %d", r, got, want[i])
		}
	}
}

func TestDriftEstimatorConstantSlope(t *testing.T) {
	d := NewDriftEstimator(5_000_000) // 5ms jump threshold
	const ppm = 250.0
	for i := int64(0); i < 200; i++ {
		local := i * 20_000_000 // 20ms cadence
		remote := local + int64(float64(local)*ppm/1e6)
		d.Add(local, remote)
	}
	got := d.DriftPPM()
	if math.Abs(got-ppm) > 5 {
		t.Fatalf("DriftPPM = %v, want ~%v", got, ppm)
	}
	if n := len(d.Segments()); n != 1 {
		t.Fatalf("segments = %d, want 1 (no false splits)", n)
	}
}

func TestDriftEstimatorAbruptJumpIsSegmented(t *testing.T) {
	d := NewDriftEstimator(5_000_000)
	// 200ppm for the first half, then an abrupt offset jump + 400ppm.
	for i := int64(0); i < 400; i++ {
		local := i * 20_000_000
		remote := local + int64(float64(local)*200/1e6)
		if i >= 200 {
			remote = local + int64(float64(local)*400/1e6) + 50_000_000
		}
		d.Add(local, remote)
	}
	segs := d.Segments()
	if len(segs) != 2 {
		t.Fatalf("segments = %d, want 2: %+v", len(segs), segs)
	}
	if math.Abs(segs[0].SlopePPM-200) > 10 {
		t.Fatalf("seg0 ppm = %v, want ~200", segs[0].SlopePPM)
	}
	if math.Abs(segs[1].SlopePPM-400) > 10 {
		t.Fatalf("seg1 ppm = %v, want ~400", segs[1].SlopePPM)
	}
	// The jump must not poison the first segment's slope.
	if segs[0].SlopePPM > 250 {
		t.Fatalf("jump leaked into first segment: %v", segs[0].SlopePPM)
	}
}

func TestTimelineNonZeroStartAndVFR(t *testing.T) {
	samples := []Sample{
		{Index: 0, CTS: 1_500_000_000, Duration: 40_000_000, Keyframe: true},
		{Index: 1, CTS: 1_540_000_000, Duration: 17_000_000},
		{Index: 2, CTS: 1_557_000_000, Duration: 83_000_000},
	}
	out, keep, err := BuildTimeline(samples, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range samples {
		if !keep[i] {
			t.Fatalf("sample %d dropped without edits", i)
		}
		if want := s.CTS - 1_500_000_000; out[i] != want {
			t.Fatalf("out[%d] = %d, want %d", i, out[i], want)
		}
	}
}

func TestTimelineEditListMerge(t *testing.T) {
	var samples []Sample
	for i := 0; i < 10; i++ {
		samples = append(samples, Sample{Index: i, CTS: int64(i) * 1_000_000_000, Duration: 1_000_000_000})
	}
	edits := []Edit{
		{MediaTime: -1, Duration: 500_000_000, Rate: 1},              // empty: 0.5s gap
		{MediaTime: 2_000_000_000, Duration: 3_000_000_000, Rate: 1}, // keep t=2,3,4
		{MediaTime: 7_000_000_000, Duration: 2_000_000_000, Rate: 1}, // keep t=7,8
	}
	out, keep, err := BuildTimeline(samples, edits, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantCTS := []int64{2, 3, 4, 7, 8}
	var gotCTS []int64
	for i := range samples {
		if keep[i] {
			gotCTS = append(gotCTS, samples[i].CTS/1_000_000_000)
		}
	}
	if len(gotCTS) != len(wantCTS) {
		t.Fatalf("kept %v, want CTS %v", gotCTS, wantCTS)
	}
	for i := range wantCTS {
		if gotCTS[i] != wantCTS[i] {
			t.Fatalf("kept %v, want CTS %v", gotCTS, wantCTS)
		}
	}
	// First kept sample starts after the 0.5s empty edit; strictly monotonic.
	if out[2] != 500_000_000 {
		t.Fatalf("first kept out = %d, want 500ms", out[2])
	}
	prev := int64(-1)
	for i := range samples {
		if !keep[i] {
			continue
		}
		if out[i] <= prev {
			t.Fatalf("output not strictly monotonic at %d", i)
		}
		prev = out[i]
	}
}

func TestTimelineInvalidEditListIsUnrecoverable(t *testing.T) {
	samples := []Sample{{Index: 0, CTS: 0, Duration: 1_000_000_000}}
	edits := []Edit{{MediaTime: 99_000_000_000, Duration: 1_000_000_000, Rate: 1}}
	_, _, err := BuildTimeline(samples, edits, 0)
	if err == nil {
		t.Fatal("expected error for media_time beyond duration")
	}
	if !IsUnrecoverable(err) {
		t.Fatalf("error must be unrecoverable, got %v", err)
	}
	var me *Error
	if !As(err, &me) || me.Phase != PhaseTimeline {
		t.Fatalf("error must carry timeline phase, got %v", err)
	}
}

func TestTimelineDeterministic(t *testing.T) {
	samples := []Sample{
		{Index: 0, CTS: 7_000_000, Duration: 3_000_000},
		{Index: 1, CTS: 7_000_000, Duration: 3_000_000}, // colliding CTS
		{Index: 2, CTS: 10_000_000, Duration: 3_000_000},
	}
	a, _, err1 := BuildTimeline(samples, nil, 0)
	b, _, err2 := BuildTimeline(samples, nil, 0)
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("non-deterministic timeline: %v vs %v", a, b)
		}
		if i > 0 && a[i] <= a[i-1] {
			t.Fatalf("not strictly monotonic: %v", a)
		}
	}
}

// As is a tiny local alias to keep the test imports obvious.
func As(err error, target **Error) bool {
	for err != nil {
		if e, ok := err.(*Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
