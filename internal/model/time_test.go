package model

import "testing"

func TestTicksFromSamples(t *testing.T) {
	cases := []struct {
		name    string
		samples int64
		rate    uint32
		want    Tick
	}{
		{"opus 20ms", 960, 48000, 20 * TickMilli},
		{"opus 2.5ms", 120, 48000, 2500},
		{"aac 1024 at 44.1k", 1024, 44100, 23_220},
		{"zero samples", 0, 48000, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := TicksFromSamples(tc.samples, tc.rate)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("TicksFromSamples(%d, %d) = %d, want %d", tc.samples, tc.rate, got, tc.want)
			}
		})
	}
}

func TestTicksFromSamplesRejectsBadInput(t *testing.T) {
	if _, err := TicksFromSamples(1, 0); err == nil {
		t.Fatal("expected error for zero rate")
	}
	if _, err := TicksFromSamples(-1, 48000); err == nil {
		t.Fatal("expected error for negative sample count")
	}
}

func TestTicksFromTimescale(t *testing.T) {
	// 90 kHz video timescale, one frame at 30 fps rounds to 33.333 ms.
	got, err := TicksFromTimescale(3000, 90000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := Tick(33_333); got != want {
		t.Fatalf("got %d, want %d", got, want)
	}
}

func TestSpanHelpers(t *testing.T) {
	a := NewSpan(10, 30)
	b := Span{Start: 20, End: 50}
	if !a.Valid() || a.Duration() != 20 {
		t.Fatalf("span validity/duration wrong: %+v", a)
	}
	if !a.Contains(10) || a.Contains(30) {
		t.Fatal("half-open containment is wrong")
	}
	if !a.Overlaps(b) {
		t.Fatal("expected overlap")
	}
	if got := a.Union(b); got.Start != 10 || got.End != 50 {
		t.Fatalf("union = %+v", got)
	}
	if NewSpan(42, 7).End != 42 {
		t.Fatal("NewSpan must clamp inverted ranges")
	}
}

func TestMergeSpans(t *testing.T) {
	spans := []Span{
		{Start: 30, End: 40},
		{Start: 10, End: 20},
		{Start: 20, End: 25},
		{Start: 60, End: 70},
		{Start: 65, End: 66},
	}
	got := MergeSpans(spans)
	want := []Span{{10, 25}, {30, 40}, {60, 70}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("at %d got %v, want %v", i, got[i], want[i])
		}
	}
	if MergeSpans(nil) != nil {
		t.Fatal("nil input should stay nil")
	}
}
