package clock

import (
	"math"
	"math/big"
	"testing"

	"mediasync/internal/fault"
)

func ratEq(t *testing.T, got *big.Rat, want float64) {
	t.Helper()
	f, _ := got.Float64()
	if math.Abs(f-want) > 1e-12 {
		t.Fatalf("got %v want %v", f, want)
	}
}

func TestTimebaseConversion(t *testing.T) {
	if _, err := NewTimebase(0); err == nil {
		t.Fatal("zero timescale must be rejected")
	} else if f, ok := fault.AsFault(err); !ok || f.Code != fault.ErrIllegalTimebase {
		t.Fatalf("want illegal_timebase fault, got %v", err)
	}

	tb, err := NewTimebase(90000)
	if err != nil {
		t.Fatal(err)
	}
	ratEq(t, tb.TicksToSeconds(45000), 0.5)
	ratEq(t, tb.TicksToMillis(45000), 500)

	// Exactness: 1001-scale values must not be rounded to binary floats.
	tb2, _ := NewTimebase(44100)
	oneSample := tb2.TicksToSeconds(1)
	product := new(big.Rat).Mul(oneSample, big.NewRat(44100, 1))
	if product.Cmp(big.NewRat(1, 1)) != 0 {
		t.Fatalf("rational arithmetic must be exact, got %v", product)
	}
}

func TestBuildMarksVFRAndOverflow(t *testing.T) {
	tb, _ := NewTimebase(1000)
	// Irregular durations: 10ms, 30ms, 5ms — no fixed frame rate assumed.
	samples := []RawSample{
		{DurationTicks: 10, Offset: 100},
		{DurationTicks: 30, Offset: 200},
		{DurationTicks: 5, Offset: 300},
	}
	marks, err := BuildMarks(tb, samples)
	if err != nil {
		t.Fatal(err)
	}
	wantTicks := []int64{0, 10, 40}
	for i, m := range marks {
		if m.RawTicks != wantTicks[i] || m.DurationTicks != int64(samples[i].DurationTicks) {
			t.Fatalf("mark %d: got ticks %d/%d", i, m.RawTicks, m.DurationTicks)
		}
		if m.CalibSeconds.Cmp(m.RawSeconds) != 0 {
			t.Fatal("uncalibrated marks must keep calib == raw")
		}
		if m.Offset != samples[i].Offset {
			t.Fatalf("mark %d lost original offset", i)
		}
	}
}

func TestDriftDetectionAndCalibration(t *testing.T) {
	// Observed clock runs 1% fast: 1000 observed ticks == 990 reference ms.
	tb, _ := NewTimebase(1000)
	anchors := []Anchor{
		{ObservedTicks: 0, RefSeconds: big.NewRat(0, 1)},
		{ObservedTicks: 1_000_000, RefSeconds: big.NewRat(990, 1)},
	}
	rep, err := DetectDrift(tb, anchors, 50_000)
	if err != nil {
		t.Fatal(err)
	}
	// Observed clock runs fast -> observed ticks outrun reference seconds,
	// so (1/slope - 1)*1e6 is positive, around +10101 PPM.
	if rep.PPM <= 9000 || rep.PPM >= 11000 {
		t.Fatalf("drift ppm outside expected band: %d", rep.PPM)
	}
	ratEq(t, rep.Calibrate(tb, 1_000_000), 990)

	marks, _ := BuildMarks(tb, []RawSample{
		{DurationTicks: 1000},
		{DurationTicks: 1000},
	})
	rep.Apply(tb, marks)
	// The origin stays put; a non-zero point must move under correction.
	if marks[0].CalibSeconds.Cmp(marks[0].RawSeconds) != 0 {
		t.Fatal("origin must remain anchored after calibration")
	}
	if marks[1].CalibSeconds.Cmp(marks[1].RawSeconds) == 0 {
		t.Fatal("calibrated value must differ from raw after drift correction")
	}
	ratEq(t, marks[1].CalibSeconds, 0.99)

	// Over threshold must fail with a drift fault.
	if _, err := DetectDrift(tb, anchors, 100); err == nil {
		t.Fatal("drift over threshold must be reported")
	} else if f, ok := fault.AsFault(err); !ok || f.Code != fault.ErrDriftTooLarge {
		t.Fatalf("want drift fault, got %v", err)
	}
}

func TestExtendRTPTimestamps(t *testing.T) {
	track := "audio"
	// Forward, a clean wrap near 2^32, and a small backward reorder delta.
	raw := []uint32{0xFFFFFFF0, 0x00000010, 0x00000020, 0x00000018}
	offsets := []int64{0, 100, 200, 300}
	ext, err := ExtendRTPTimestamps(track, raw, offsets)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{0xFFFFFFF0, 0x100000010, 0x100000020, 0x100000018}
	for i := range want {
		if ext[i] != want[i] {
			t.Fatalf("index %d: got %d want %d", i, ext[i], want[i])
		}
	}

	// A 2^31 jump is ambiguous and must be reported with its offset.
	ambRaw := []uint32{0, 1 << 31}
	_, err = ExtendRTPTimestamps(track, ambRaw, []int64{0, 4242})
	f, ok := fault.AsFault(err)
	if !ok || f.Code != fault.ErrWrapAmbiguous || f.Offset != 4242 {
		t.Fatalf("want ambiguous-wrap fault at offset 4242, got %v", err)
	}
}

func TestMaxAbsDiffAndIsHalfWrap(t *testing.T) {
	a := []*big.Rat{big.NewRat(1, 1), big.NewRat(2, 1)}
	b := []*big.Rat{big.NewRat(1, 1), big.NewRat(25, 10)}
	worst, idx := MaxAbsDiffMillis(a, b)
	if idx != 1 || math.Abs(worst-500) > 1e-9 {
		t.Fatalf("got worst=%f idx=%d", worst, idx)
	}
	if !IsHalfWrap(0, 1<<31) || !IsHalfWrap(1<<31, 0) {
		t.Fatal("half-wrap detection failed")
	}
	if IsHalfWrap(0, 10) {
		t.Fatal("small delta is not a half wrap")
	}
}
