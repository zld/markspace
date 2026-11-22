package media

import (
	"errors"
	"math"
	"testing"
)

func TestRationalConvert(t *testing.T) {
	cases := []struct {
		name       string
		value      int64
		from, to   Rational
		want       int64
	}{
		{"44.1k ticks to 48k", 441, TimeScale(44100), TimeScale(48000), 480},
		{"48k to 44.1k", 480, TimeScale(48000), TimeScale(44100), 441},
		{"tick to nanos", 1, TimeScale(48000), Rational{NanosecondsPerSecond, 1}, 20833},
		{"nanos to 90k", 1_000_000, Rational{NanosecondsPerSecond, 1}, TimeScale(90000), 90},
		{"half rounds away from zero", 1, TimeScale(2), TimeScale(1), 1},
		{"small fraction rounds down", 49, Rational{100, 1}, Rational{1000, 1}, 490},
	}
	for _, c := range cases {
		got, err := Convert(c.value, c.from, c.to)
		if err != nil {
			t.Fatalf("%s: unexpected error %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}

func TestConvertRoundTripStable(t *testing.T) {
	src := TimeScale(44100)
	dst := TimeScale(48000)
	for ticks := int64(0); ticks < 100000; ticks += 7 {
		ns, err := ToNanos(ticks, src)
		if err != nil {
			t.Fatal(err)
		}
		back, err := FromNanos(ns, src)
		if err != nil {
			t.Fatal(err)
		}
		if back != ticks {
			t.Fatalf("round trip mismatch at %d: got %d", ticks, back)
		}
		if _, err := Convert(ticks, src, dst); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConvertRejectsOverflowAndBadScale(t *testing.T) {
	if _, err := Convert(math.MaxInt64, TimeScale(1), TimeScale(math.MaxInt32)); err == nil {
		t.Fatal("expected overflow error")
	}
	if _, err := Convert(1, Rational{0, 1}, TimeScale(1)); err == nil {
		t.Fatal("expected invalid scale error")
	}
}

func TestIntervalInvariants(t *testing.T) {
	good := Interval{Start: 10, End: 20}
	if !good.Valid() || good.Duration() != 10 || !good.Contains(15) || good.Contains(20) {
		t.Fatal("basic interval semantics broken")
	}
	bad := Interval{Start: 20, End: 10}
	if bad.Valid() {
		t.Fatal("reversed interval accepted")
	}
	if !good.Overlaps(Interval{15, 25}) || good.Overlaps(Interval{20, 30}) {
		t.Fatal("half-open overlap semantics broken")
	}
	if m := good.Merge(Interval{25, 30}); m != (Interval{10, 30}) {
		t.Fatalf("merge got %v", m)
	}
	if _, ok := AssertMonotonic(100, 99); ok {
		t.Fatal("rollback accepted as monotonic")
	}
	if adv, ok := AssertMonotonic(100, 140); !ok || adv != 40 {
		t.Fatal("monotonic advance misreported")
	}
}

func TestErrorModel(t *testing.T) {
	iv := Interval{Start: 5, End: 9}
	err := Errorf(CodeMissingTable, 7, &iv, 4096, "stsz absent in %q", "moov/trak")
	if !err.Rejectable() || err.TrackID != 7 || err.ByteOffset != 4096 {
		t.Fatal("error fields lost")
	}
	wrapped := err.Wrap(errors.New("underlying"))
	me, ok := AsError(wrapped)
	if !ok || me.Code != CodeMissingTable {
		t.Fatal("errors.As failed to recover media.Error")
	}
	if !RejectableError(wrapped) || RejectableError(errors.New("plain")) {
		t.Fatal("rejectable classification wrong")
	}
	if me.Error() == "" {
		t.Fatal("empty error string")
	}
	repairable := NewError(CodeLatePacket, 1, nil, -1, "window expired")
	if repairable.Rejectable() {
		t.Fatal("late packet must not be input-rejecting")
	}
}
