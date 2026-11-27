package clock

import (
	"math"
	"testing"
	"time"
)

func TestTickConversion(t *testing.T) {
	ms, err := FromMillis(200)
	if err != nil {
		t.Fatalf("FromMillis: %v", err)
	}
	if ms != GapThresholdTicks {
		t.Fatalf("200ms = %d ticks, want %d", ms, GapThresholdTicks)
	}
	if ms.Millis() != 200 {
		t.Fatalf("Millis = %d, want 200", ms.Millis())
	}
	if got := ms.Seconds(); got != 0.2 {
		t.Fatalf("Seconds = %v, want 0.2", got)
	}
	if _, err := FromMillis(math.MaxInt64); err == nil {
		t.Fatal("FromMillis(MaxInt64) should overflow")
	}
}

func TestAddSubOverflow(t *testing.T) {
	if _, err := Add(Tick(math.MaxInt64), 1); err == nil {
		t.Fatal("Add should overflow")
	}
	if _, err := Sub(Tick(math.MinInt64), 1); err == nil {
		t.Fatal("Sub should overflow")
	}
	got, err := Sub(Tick(90000), Tick(18000))
	if err != nil || got != 72000 {
		t.Fatalf("Sub = %d, %v", got, err)
	}
	if err := Tick(math.MaxInt64 / 2).Validate(); err == nil {
		t.Fatal("Validate should reject unsafe value")
	}
}

func TestFromDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want Tick
	}{
		{0, 0},
		{time.Millisecond, 90},
		{time.Second, 90000},
		{-time.Second, -90000},
		{1500 * time.Microsecond, 135},
	}
	for _, tc := range cases {
		got, err := FromDuration(tc.d)
		if err != nil {
			t.Fatalf("FromDuration(%v): %v", tc.d, err)
		}
		if got != tc.want {
			t.Fatalf("FromDuration(%v) = %d, want %d", tc.d, got, tc.want)
		}
	}
}

func TestUnwrapperWrap(t *testing.T) {
	var u Unwrapper
	seq := []uint32{0xFFFFF000, 0xFFFFF800, 0x00000000, 0x00000800, 0xFFFFFF00}
	want := []Tick{4294963200, 4294965248, 4294967296, 4294969344, 4294967040}
	for i, ts := range seq {
		got, err := u.Unwrap(ts)
		if err != nil {
			t.Fatalf("Unwrap(%#x): %v", ts, err)
		}
		if got != want[i] {
			t.Fatalf("Unwrap(%#x) = %d, want %d", ts, got, want[i])
		}
	}
}

func TestUnwrapperOutOfOrder(t *testing.T) {
	var u Unwrapper
	first, _ := u.Unwrap(3000)
	second, _ := u.Unwrap(9000)
	back, _ := u.Unwrap(6000)
	fourth, _ := u.Unwrap(12000)
	if first != 3000 || second != 9000 || back != 6000 || fourth != 12000 {
		t.Fatalf("unexpected expansion: %d %d %d %d", first, second, back, fourth)
	}
	u.Reset()
	again, _ := u.Unwrap(500)
	if again != 500 {
		t.Fatalf("Reset did not clear state: %d", again)
	}
}
