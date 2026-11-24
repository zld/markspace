package clock

import "testing"

func TestConvertRound(t *testing.T) {
	cases := []struct {
		name     string
		ticks    Ticks
		from, to uint32
		want     Ticks
	}{
		{"identity", 123, 90000, 90000, 123},
		{"ms to 90k", 1000, 1000, 90000, 90000},
		{"90k to ms", 180000, 90000, 1000, 2000},
		{"round up half", 1, 3, 2, 1}, // 2/3 -> 1
		{"round down", 1, 1000, 90, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ConvertRound(tc.ticks, tc.from, tc.to); got != tc.want {
				t.Fatalf("ConvertRound()=%d want %d", got, tc.want)
			}
		})
	}
}

func TestCompareAcrossTimescales(t *testing.T) {
	a := NewTimestamp(90000, 90000) // 1s
	b := NewTimestamp(1000, 1000)   // 1s
	c := NewTimestamp(47999, 48000) // just before 1s
	if got := Compare(a, b); got != 0 {
		t.Fatalf("1s timestamps compare=%d, want 0", got)
	}
	if got := Compare(c, b); got != -1 {
		t.Fatalf("47999/48000 compare to 1s =%d, want -1", got)
	}
	if d := AbsDiffMillis(NewTimestamp(0, 90000), NewTimestamp(45, 90000)); d < 0.49 || d > 0.51 {
		t.Fatalf("AbsDiffMillis()=%v, want about 0.5", d)
	}
}

func TestTimebaseMillis(t *testing.T) {
	tb, err := NewTimebase(48000)
	if err != nil {
		t.Fatal(err)
	}
	if got := tb.FromMillis(20); got != 960 {
		t.Fatalf("FromMillis(20)=%d want 960", got)
	}
	if got := tb.ToMillis(960); got != 20 {
		t.Fatalf("ToMillis(960)=%d want 20", got)
	}
}
