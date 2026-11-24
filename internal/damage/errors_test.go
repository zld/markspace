package damage

import "testing"

func TestRangeOverlaps(t *testing.T) {
	cases := []struct {
		name string
		a, b Range
		want bool
	}{
		{"disjoint", Range{0, 10, true, ""}, Range{10, 20, true, ""}, false},
		{"touching half-open", Range{0, 10, true, ""}, Range{10, 20, true, ""}, false},
		{"overlap", Range{0, 15, true, ""}, Range{10, 20, true, ""}, true},
		{"contained", Range{0, 100, true, ""}, Range{10, 20, true, ""}, true},
	}
	for _, c := range cases {
		if got := c.a.Overlaps(c.b); got != c.want {
			t.Errorf("%s: Overlaps = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestErrorAndSetRecoverable(t *testing.T) {
	e := (&Error{
		Code:    CodeTruncatedBox,
		TrackID: 2,
		Range:   Range{StartNanos: 100, EndNanos: 200, Deterministic: false},
		Detail:  "moov/trak/mdia/minf/stbl/trun cut at byte 412",
	}).SetRecoverable(RecoverableNo)
	if e.Recoverable != RecoverableNo || e.Range.Recoverable != RecoverableNo {
		t.Fatal("SetRecoverable must stamp both error and range")
	}
	if s := e.Error(); s == "" {
		t.Fatal("Error() must render a message")
	}
	if r := (Range{EndNanos: 50, StartNanos: 10}); r.LengthNanos() != 40 {
		t.Fatal("LengthNanos mismatch")
	}
}
