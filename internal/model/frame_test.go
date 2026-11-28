package model

import "testing"

func TestRationalTickMicros(t *testing.T) {
	// 90000 ticks/sec: 45000 ticks == 500000 us.
	r := Rational{Num: 90000, Den: 1}
	got, ok := r.TickMicros(45000)
	if !ok || got != 500000 {
		t.Fatalf("TickMicros(45000) = %d,%v want 500000,true", got, ok)
	}
	// 30000/1001 media base: one tick == 1001/30000 s.
	r2 := Rational{Num: 30000, Den: 1001}
	if _, ok := r2.TickMicros(0); !ok {
		t.Fatal("zero tick should convert")
	}
	if (Rational{}).Valid() {
		t.Fatal("zero rational must be invalid")
	}
}

func TestNewTimestamp(t *testing.T) {
	off := RawOffset{Stream: "s", Start: 10, End: 42}
	ts := NewTimestamp(90, Rational{Num: 90000, Den: 1}, off)
	if !ts.HasMicros || ts.Micros != 1000 {
		t.Fatalf("micros = %d has=%v", ts.Micros, ts.HasMicros)
	}
	if ts.Offset.Start != 10 {
		t.Fatal("offset must be preserved")
	}
}

func TestCodeErrorRoundTrip(t *testing.T) {
	ce := ChecksumError(RawOffset{Start: 7, End: 9}, 1, 2, 3)
	var err error = ce
	back := AsCodeError(err)
	if back == nil || back.Kind != KindChecksum || back.FirstPos != 7 {
		t.Fatalf("unwrapped error = %+v", back)
	}
	if AsCodeError(nil) != nil {
		t.Fatal("nil must unwrap to nil")
	}
}

func TestFrameTypeString(t *testing.T) {
	if TypeKeyFrame.String() != "key" || TypeDelta.String() != "delta" ||
		TypeUnknown.String() != "unknown" {
		t.Fatal("unexpected frame type rendering")
	}
}
