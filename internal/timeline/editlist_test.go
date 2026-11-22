package timeline

import "testing"

// TestNonZeroStartOffset verifies that a container whose first sample carries a
// non-zero media timestamp still maps onto a timeline starting at zero.
func TestNonZeroStartOffset(t *testing.T) {
	const offset = 5 * Second // media time zero of the first sample
	axis := NewAxis(offset)
	if err := axis.IdentityEdit(35 * Second); err != nil {
		t.Fatalf("IdentityEdit: %v", err)
	}
	got, ok := axis.ToTimeline(5 * Second)
	if !ok || got != 0 {
		t.Fatalf("first sample maps to %d (ok=%v), want 0", got, ok)
	}
	got, ok = axis.ToTimeline(35*Second - Microsecond)
	if !ok || got != 30*Second-Microsecond {
		t.Fatalf("last sample maps to %d (ok=%v)", got, ok)
	}
	// Samples before the start offset are not presented.
	if _, ok := axis.ToTimeline(offset - 1); ok {
		t.Fatal("sample before start offset must not be presented")
	}
}

// TestEditListOffsetAcrossManifestAlignment models the required fault class:
// manifest-declared cut points, expressed in media-local container time, must
// land on shifted timeline coordinates within the alignment budget; the
// per-point deviation is computed on the unified axis and stays bounded.
func TestEditListOffsetAcrossManifestAlignment(t *testing.T) {
	const offset = 1200 * Millisecond // non-zero container start
	axis := NewAxis(offset)
	// Edit list shifts presentation by +800 ms relative to media time.
	if err := axis.AddEdit(Edit{
		MediaStart: offset,
		Start:      800 * Millisecond,
		Duration:   10 * Second,
	}); err != nil {
		t.Fatalf("AddEdit: %v", err)
	}

	// Manifest-declared cut points in media-local time and the naive timeline
	// coordinates the manifest would assume without edit-list accounting.
	type point struct{ declared, naive Time }
	points := []point{
		{offset + 0, 0},
		{offset + 2*Second + 500*Millisecond, 2*Second + 500*Millisecond},
		{offset + 9*Second + 999*Millisecond, 9*Second + 999*Millisecond},
	}
	const budget = 40 * Millisecond
	const shift = 800 * Millisecond
	var maxDev Time
	for _, p := range points {
		mapped, ok := axis.ToTimeline(p.declared)
		if !ok {
			t.Fatalf("declared point %d not presented", p.declared)
		}
		// Correct expected coordinate applies the edit-list shift to the naive
		// manifest coordinate.
		want, err := Add(p.naive, shift)
		if err != nil {
			t.Fatal(err)
		}
		dev := mapped - want
		if dev < 0 {
			dev = -dev
		}
		if dev > maxDev {
			maxDev = dev
		}
	}
	if maxDev > budget {
		t.Fatalf("residual alignment deviation %d exceeds budget %d", maxDev, budget)
	}
	// Explicit check on the middle point.
	shifted, _ := axis.ToTimeline(points[1].declared)
	wantShifted := points[1].naive + shift
	if shifted != wantShifted {
		t.Fatalf("edit shift not applied: got %d want %d", shifted, wantShifted)
	}
	// Round-trip every presented point through both directions.
	for tt := axis.edits[0].Start; tt < axis.edits[0].End(); tt += 7 * Millisecond {
		m, ok := axis.ToMedia(tt)
		if !ok {
			t.Fatalf("ToMedia(%d) failed", tt)
		}
		back, ok := axis.ToTimeline(m)
		if !ok || back != tt {
			t.Fatalf("round trip at %d -> media %d -> %d (ok=%v)", tt, m, back, ok)
		}
	}
	if maxDev > budget {
		t.Fatalf("alignment deviation %d exceeds budget %d", maxDev, budget)
	}
}

func TestEditListValidation(t *testing.T) {
	axis := NewAxis(Second)
	if err := axis.AddEdit(Edit{MediaStart: Second, Start: 0, Duration: 0}); Classify(err) != KindInvalidInput {
		t.Fatalf("zero duration: %v", err)
	}
	if err := axis.AddEdit(Edit{MediaStart: 0, Start: 0, Duration: Second}); Classify(err) != KindInvalidInput {
		t.Fatalf("media start before offset: %v", err)
	}
	e1 := Edit{MediaStart: Second, Start: 0, Duration: 5 * Second}
	e2 := Edit{MediaStart: 7 * Second, Start: 4 * Second, Duration: 5 * Second} // overlaps
	if err := axis.AddEdit(e1); err != nil {
		t.Fatalf("e1: %v", err)
	}
	if err := axis.AddEdit(e2); Classify(err) != KindInvalidInput {
		t.Fatalf("overlap not rejected: %v", err)
	}
}

func TestRepeatedEditKeepsDistinctCoordinates(t *testing.T) {
	axis := NewAxis(0)
	a := Edit{MediaStart: 0, Start: 0, Duration: 2 * Second}
	b := Edit{MediaStart: 0, Start: 2 * Second, Duration: 2 * Second} // same media range
	if err := axis.AddEdit(a); err != nil {
		t.Fatal(err)
	}
	if err := axis.AddEdit(b); err != nil {
		t.Fatalf("repeated media range must be allowed: %v", err)
	}
	// The same media-local instant is presented twice at distinct timeline
	// coordinates; the inverse mapping proves both presentations point back at
	// the same source range (source mapping must be preserved downstream).
	m0, ok := axis.ToMedia(500 * Millisecond)
	if !ok || m0 != 500*Millisecond {
		t.Fatalf("first presentation mapping wrong: %d (ok=%v)", m0, ok)
	}
	m1, ok := axis.ToMedia(2500 * Millisecond)
	if !ok || m1 != 500*Millisecond {
		t.Fatalf("repeated presentation mapping wrong: %d (ok=%v)", m1, ok)
	}
	// Forward mapping of a media-local instant resolves to the first edit only.
	tl, ok := axis.ToTimeline(500 * Millisecond)
	if !ok || tl != 500*Millisecond {
		t.Fatalf("forward mapping wrong: %d (ok=%v)", tl, ok)
	}
	if d := axis.TimelineDuration(); d != 4*Second {
		t.Fatalf("duration = %d, want 4s", d)
	}
}
