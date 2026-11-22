package clock

import (
	"testing"

	"mediad/internal/core"
)

func TestFitDriftRejectsImplausibleIntercept(t *testing.T) {
	m := DriftModel{Slope: core.Rat(1, 1), Intercept: core.Rat(7200, 1), Applied: true}
	if err := m.Validate(); err == nil {
		t.Fatal("want implausible intercept rejection")
	}
}

func TestCorrectWithoutAppliedIsIdentity(t *testing.T) {
	m := DriftModel{Slope: core.Rat(1001, 1000), Intercept: core.Rat(5, 1)}
	got := m.Correct(core.Rat(42, 1))
	if got.Cmp(core.Rat(42, 1)) != 0 {
		t.Fatalf("unapplied model changed the clock: %s", got)
	}
}

func TestCorrectorAccumulatesApplied(t *testing.T) {
	c := NewCorrector(DriftModel{Slope: core.Rat(1, 1), Intercept: core.Zero(), Applied: true})
	for i := int64(0); i < 5; i++ {
		c.Correct(core.Rat(i, 1), false)
	}
	if c.Applied() != 5 {
		t.Fatalf("applied=%d", c.Applied())
	}
	if c.Clamps() != 0 {
		t.Fatalf("clamps=%d", c.Clamps())
	}
}

func TestWrapEventCountsAcrossManyRollovers(t *testing.T) {
	var e Extender
	// Two rollovers using 2^29-sized steps. A step of exactly 2^31 is
	// inherently ambiguous (the signed delta is negative), so it is treated
	// as a regression, not a wrap.
	step := int64(1 << 29)
	var raw uint32
	e.Extend(raw)
	for i := 0; i < 7; i++ {
		raw += uint32(step)
		e.Extend(raw)
	}
	for wrap := 0; wrap < 2; wrap++ {
		for i := 0; i < 8; i++ {
			raw += uint32(step)
			e.Extend(raw)
		}
	}
	if e.Wraps() != 2 {
		t.Fatalf("wraps=%d", e.Wraps())
	}
}

func TestTimelineSealRejectsDuplicateTrack(t *testing.T) {
	b := NewBuilder("a")
	if err := b.AddTrack(Track{Name: "a", Asset: "x", Scale: core.Rat(1, 1)}); err != nil {
		t.Fatal(err)
	}
	if err := b.AddTrack(Track{Name: "a", Asset: "y", Scale: core.Rat(1, 1)}); err == nil {
		t.Fatal("want duplicate track rejection")
	}
	if err := b.AddTrack(Track{Name: "b", Asset: "y", Scale: core.Zero()}); err == nil {
		t.Fatal("want zero scale rejection")
	}
}

func TestTimelineValidateRequiresKnownMaster(t *testing.T) {
	tl := &Timeline{Master: "missing", Tracks: []Track{{Name: "a", Asset: "x"}}}
	if err := tl.Validate(); err == nil {
		t.Fatal("want unknown master rejection")
	}
}
