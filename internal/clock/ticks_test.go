package clock

import "testing"

func TestToMasterExact(t *testing.T) {
	// 2 seconds at timescale 1000 -> 180000 master ticks.
	got, err := ToMaster(2000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got != 180000 {
		t.Fatalf("got %d, want 180000", got)
	}
}

func TestToMasterRounding(t *testing.T) {
	// 1 unit at timescale 3 -> 30000 ticks exactly.
	got, err := ToMaster(1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got != 30000 {
		t.Fatalf("got %d, want 30000", got)
	}
	// 1 unit at timescale 7 -> 90000/7 = 12857.142... -> 12857.
	got, err = ToMaster(1, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got != 12857 {
		t.Fatalf("got %d, want 12857", got)
	}
}

func TestToMasterInvalidTimescale(t *testing.T) {
	if _, err := ToMaster(10, 0); err == nil {
		t.Fatal("expected error for zero timescale")
	}
	if _, err := ToMaster(10, -5); err == nil {
		t.Fatal("expected error for negative timescale")
	}
}

func TestRoundTrip(t *testing.T) {
	for _, ts := range []int64{1000, 90000, 44100, 48000, 12800} {
		in := int64(123456789)
		m, err := ToMaster(in, ts)
		if err != nil {
			t.Fatal(err)
		}
		back := m.To(ts)
		if d := back - in; d < -1 || d > 1 {
			t.Fatalf("timescale %d: round trip %d -> %d -> %d", ts, in, m, back)
		}
	}
}

func TestParseDurationIntegerArithmetic(t *testing.T) {
	// Accumulating 100 segments of 9.976s must be exact integer math.
	d, err := ParseDuration("9.976")
	if err != nil {
		t.Fatal(err)
	}
	var total Ticks
	for range 100 {
		total += d
	}
	// 9.976 * 100 = 997.6 s -> 89784000 ticks exactly.
	if total != 89784000 {
		t.Fatalf("got %d, want 89784000", total)
	}
}

func TestParseDecimal(t *testing.T) {
	num, den, err := ParseDecimal("1.001")
	if err != nil {
		t.Fatal(err)
	}
	if num != 1001 || den != 1000 {
		t.Fatalf("got %d/%d", num, den)
	}
	if _, _, err := ParseDecimal("abc"); err == nil {
		t.Fatal("expected error")
	}
	if _, _, err := ParseDecimal("1."); err == nil {
		t.Fatal("expected error for trailing dot")
	}
}

func TestMillis(t *testing.T) {
	if got := FromMillis(1000); got != 90000 {
		t.Fatalf("got %d", got)
	}
	if got := Ticks(45000).Millis(); got != 500 {
		t.Fatalf("got %d", got)
	}
}
