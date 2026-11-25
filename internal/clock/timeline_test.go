package clock

import "testing"

func TestTimelineStableSortAndOverlaps(t *testing.T) {
	var tl Timeline
	items := []Interval{
		{Start: 20, End: 30, Order: 0, Label: "later"},
		{Start: 10, End: 25, Order: 1, Label: "first-start"},
		{Start: 10, End: 12, Order: 2, Label: "same-start-later-order"},
		{Start: 30, End: 40, Order: 3, Label: "touching"},
	}
	for _, in := range items {
		if err := tl.Add(in); err != nil {
			t.Fatalf("Add(%v): %v", in.Label, err)
		}
	}
	if err := tl.Normalize(); err != nil {
		t.Fatal(err)
	}

	gotOrder := []int{tl.Intervals[0].Order, tl.Intervals[1].Order, tl.Intervals[2].Order, tl.Intervals[3].Order}
	wantOrder := []int{1, 2, 0, 3}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("sort order = %v, want %v", gotOrder, wantOrder)
		}
	}

	overlaps := tl.Overlaps()
	if len(overlaps) != 2 {
		t.Fatalf("overlaps = %v, want two stable pairs", overlaps)
	}
	if overlaps[0] != (Overlap{A: 1, B: 2}) || overlaps[1] != (Overlap{A: 1, B: 0}) {
		t.Fatalf("unexpected overlaps: %v", overlaps)
	}
	if tl.Duration() != 40 {
		t.Fatalf("Duration = %d, want 40", tl.Duration())
	}
}

func TestTimelineRejectsNegativeAndReversed(t *testing.T) {
	var tl Timeline
	err := tl.Add(Interval{Start: -1, End: 2, Sample: 4, Offset: 9})
	if err == nil || !contains(err.Error(), "NEGATIVE_TIME") {
		t.Fatalf("negative interval error = %v", err)
	}
	err = tl.Add(Interval{Start: 5, End: 4, Sample: 5, Offset: 11})
	if err == nil || !contains(err.Error(), "INVALID_TIMECODE") {
		t.Fatalf("reversed interval error = %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
