package cue

import "testing"

func TestPartsRoundTrip(t *testing.T) {
	for _, ms := range []int64{0, 1, 999, 61001, 3600000, 3661999, 359999999} {
		h, m, s, milli := SplitParts(ms)
		got, err := FromParts(h, m, s, milli)
		if err != nil {
			t.Fatalf("FromParts(%d): %v", ms, err)
		}
		if got != ms {
			t.Fatalf("round trip %d -> %d", ms, got)
		}
	}
}

func TestFromPartsRejectsBadRanges(t *testing.T) {
	for _, p := range [][4]int64{{0, 60, 0, 0}, {0, 0, 60, 0}, {0, 0, 0, 1000}, {-1, 0, 0, 0}} {
		if _, err := FromParts(p[0], p[1], p[2], p[3]); err == nil {
			t.Fatalf("expected error for %v", p)
		}
	}
}

func TestFormats(t *testing.T) {
	if got := FormatSRT(3661999); got != "01:01:01,999" {
		t.Fatalf("FormatSRT = %q", got)
	}
	if got := FormatVTT(3661999); got != "01:01:01.999" {
		t.Fatalf("FormatVTT = %q", got)
	}
}

func TestValidate(t *testing.T) {
	good := Cue{StartMs: 0, EndMs: 1, Lines: []string{"x"}}
	if err := good.Validate(); err != nil {
		t.Fatalf("good cue rejected: %v", err)
	}
	for _, bad := range []Cue{
		{StartMs: 5, EndMs: 5, Lines: []string{"x"}},
		{StartMs: 6, EndMs: 5, Lines: []string{"x"}},
		{StartMs: 0, EndMs: 1},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("bad cue %+v accepted", bad)
		}
	}
}
