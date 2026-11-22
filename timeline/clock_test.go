package timeline

import "testing"

func TestClockRoundTripNoDrift(t *testing.T) {
	c, err := NewClock(48000)
	if err != nil {
		t.Fatal(err)
	}
	// One minute of audio converted back and forth in small steps must not
	// drift: integer rounding is applied per conversion, never accumulated.
	total := int64(48000 * 60)
	var nanos int64
	for s := int64(0); s < total; s += 480 {
		nanos += c.SamplesToNanos(480)
	}
	back := c.NanosToSamples(nanos)
	if back != total {
		t.Fatalf("accumulated conversion drifted: got %d samples, want %d", back, total)
	}
}

func TestClockKnownConversions(t *testing.T) {
	c, _ := NewClock(48000)
	cases := []struct {
		samples int64
		nanos   int64
	}{
		{0, 0},
		{48, 1_000_000},      // 1 ms at 48 kHz
		{48000, 1_000_000_000},
		{24000, 500_000_000},
		{1, 20833}, // 1/48000 s, rounded to nearest nanosecond
	}
	for _, tc := range cases {
		if got := c.SamplesToNanos(tc.samples); got != tc.nanos {
			t.Errorf("SamplesToNanos(%d) = %d, want %d", tc.samples, got, tc.nanos)
		}
		if got := c.NanosToSamples(tc.nanos); got != tc.samples {
			t.Errorf("NanosToSamples(%d) = %d, want %d", tc.nanos, got, tc.samples)
		}
	}
}

func TestClockZeroRateRejected(t *testing.T) {
	if _, err := NewClock(0); err == nil {
		t.Fatal("zero sample rate must be rejected")
	}
}

func TestIntervalRelations(t *testing.T) {
	a := Interval{Start: 0, End: 100}
	b := Interval{Start: 100, End: 200}
	c := Interval{Start: 150, End: 160}
	d := Interval{Start: 50, End: 90}
	if !a.Adjacent(b) || a.Overlaps(b) {
		t.Fatal("touching intervals must be adjacent, not overlapping")
	}
	if !b.Contains(c) || !a.Contains(d) {
		t.Fatal("containment check wrong")
	}
	if !a.Overlaps(d) || !b.Overlaps(c) {
		t.Fatal("overlap check wrong")
	}
	g, ok := a.Gap(b)
	if ok {
		t.Fatalf("adjacent intervals have no gap, got %v", g)
	}
	gap, ok := (Interval{Start: 0, End: 10}).Gap(Interval{Start: 20, End: 30})
	if !ok || gap.Length() != 10 {
		t.Fatalf("gap = %v ok=%v, want 10-sample gap", gap, ok)
	}
	if err := (Interval{Start: 9, End: 8}).Validate("t1"); err == nil {
		t.Fatal("negative-length interval must fail validation")
	}
}

func TestIntervalAlignMillis(t *testing.T) {
	c, _ := NewClock(48000)
	iv := Interval{Start: 0, End: 48000}
	if got := iv.AlignMillis(c, 1_000_000_000); got != 0 {
		t.Fatalf("perfect alignment reported %d ms delta", got)
	}
	if got := iv.AlignMillis(c, 1_000_500_000); got != 0 {
		// sub-millisecond discrepancy must round to zero whole ms
		t.Fatalf("sub-ms delta reported as %d ms", got)
	}
	if got := iv.AlignMillis(c, 998_000_000); got != 2 {
		t.Fatalf("2 ms delta reported as %d", got)
	}
}

func TestTrackValidationFailures(t *testing.T) {
	base := func(blocks []Block) *Track {
		return &Track{ID: "t1", Rate: 48000, Channels: 2, Blocks: blocks}
	}
	cases := []struct {
		name   string
		blocks []Block
		code   ErrorCode
	}{
		{"backwards", []Block{
			{Sequence: 0, Interval: Interval{100, 200}},
			{Sequence: 1, Interval: Interval{90, 150}},
		}, ErrCodeTimestampBackwards},
		{"duplicate", []Block{
			{Sequence: 0, Interval: Interval{100, 200}},
			{Sequence: 1, Interval: Interval{100, 300}},
		}, ErrCodeDuplicateTimestamp},
		{"overlap", []Block{
			{Sequence: 0, Interval: Interval{0, 100}},
			{Sequence: 1, Interval: Interval{99, 200}},
		}, ErrCodeOverlap},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := base(tc.blocks).Validate()
			pe, ok := AsError(err)
			if !ok || pe.Code != tc.code {
				t.Fatalf("got %v, want code %s", err, tc.code)
			}
			if pe.TrackID != "t1" || pe.Stage != StageTimeline {
				t.Fatalf("error must carry track and stage: %+v", pe)
			}
		})
	}
}

func TestTrackOutOfBoundsAndGaps(t *testing.T) {
	tr := &Track{
		ID: "t1", Rate: 48000, Channels: 1,
		Expected: Interval{0, 1000},
		Blocks: []Block{
			{Sequence: 0, Interval: Interval{0, 100}},
			{Sequence: 1, Interval: Interval{200, 300}},
		},
	}
	if err := tr.Validate(); err != nil {
		t.Fatalf("gaps alone must not fail validation: %v", err)
	}
	gaps := tr.Gaps()
	if len(gaps) != 2 || gaps[0].Length() != 100 || gaps[1].Length() != 700 {
		t.Fatalf("gaps = %v, want [100,700]", gaps)
	}
	tr.Blocks = append(tr.Blocks, Block{Sequence: 2, Interval: Interval{900, 1001}})
	if err := tr.Validate(); err == nil {
		t.Fatal("block escaping expected interval must be rejected")
	}
}

func TestNewTrackSetRejectsDuplicates(t *testing.T) {
	c, _ := NewClock(48000)
	t1 := &Track{ID: "x", Rate: 48000, Channels: 1}
	if _, err := NewTrackSet(c, []*Track{t1, t1}); err == nil {
		t.Fatal("duplicate track ids must be rejected")
	}
}
