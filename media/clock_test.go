package media

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFromTicksExact(t *testing.T) {
	cases := []struct {
		ticks     int64
		timescale uint32
		want      Time
	}{
		{0, 30000, 0},
		{1, 30000, 33_333},            // 1/30000 s = 33333.33ns, rounded to 33333ns
		{1000, 30000, 33_333_333},     // 1/30 s = 33333333.33ns, rounded
		{90000, 90000, Time(NanoDen)}, // exactly one second
		{1, 1_000_000_000, 1},
		{3, 2, 1_500_000_000},
	}
	for _, c := range cases {
		got, err := FromTicks(c.ticks, c.timescale)
		if err != nil {
			t.Fatalf("FromTicks(%d,%d): %v", c.ticks, c.timescale, err)
		}
		// Conversions round to the nearest nanosecond, so allow one nanosecond
		// of difference from the mathematically exact rational value.
		if diff := int64(got) - int64(c.want); diff > 1 || diff < -1 {
			t.Fatalf("FromTicks(%d,%d)=%d want %d", c.ticks, c.timescale, got, c.want)
		}
	}
}

func TestFromTicksNoFloatAccumulation(t *testing.T) {
	// 1000 ticks at 30 kHz = 1/30 s; 300 frames must be exactly 10 s.
	const frameTicks = 1000
	const timescale = 30000
	var last Time
	for i := 1; i <= 300; i++ {
		got, err := FromTicks(int64(i*frameTicks), timescale)
		if err != nil {
			t.Fatal(err)
		}
		last = got
	}
	if last != 10*Time(NanoDen) {
		t.Fatalf("absolute conversion drifted: %d", last)
	}
}

func TestFromTicksRejectsBadInput(t *testing.T) {
	if _, err := FromTicks(1, 0); err == nil {
		t.Fatal("expected error for zero timescale")
	}
	if _, err := FromTicks(-1, 10); err == nil {
		t.Fatal("expected error for negative ticks")
	}
	if _, err := FromTicks(1<<62, 1); err == nil {
		t.Fatal("expected overflow error")
	}
}

func TestParseTime(t *testing.T) {
	cases := []struct {
		in   string
		want Time
	}{
		{"2s", Time(2 * NanoDen)},
		{"2000ms", Time(2 * NanoDen)},
		{"0.5s", 500_000_000},
		{"1500us", 1_500_000},
		{"1m", Time(60 * NanoDen)},
		{"90", Time(90 * NanoDen)},
	}
	for _, c := range cases {
		got, err := ParseTime(c.in)
		if err != nil {
			t.Fatalf("ParseTime(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("ParseTime(%q)=%d want %d", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "abc", "s", "1..5s"} {
		if _, err := ParseTime(bad); err == nil {
			t.Fatalf("ParseTime(%q) should fail", bad)
		}
	}
}

func TestTimeJSONIsIntegerMicros(t *testing.T) {
	b, err := json.Marshal(Time(2_000_000_000))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "2000000" {
		t.Fatalf("json = %s", b)
	}
	var back Time
	if err := json.Unmarshal([]byte("250000"), &back); err != nil {
		t.Fatal(err)
	}
	if back.Duration() != 250*time.Millisecond {
		t.Fatalf("round trip = %v", back.Duration())
	}
}

func TestFrameInterval(t *testing.T) {
	f, err := NewFrameInterval(1000, 30000)
	if err != nil {
		t.Fatal(err)
	}
	if f.Media() != Time(33_333_333) {
		t.Fatalf("interval = %d", f.Media())
	}
}
