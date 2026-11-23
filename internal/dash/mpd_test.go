package dash

import (
	"errors"
	"testing"

	"mediacheck/internal/fixture"
	"mediacheck/internal/model"
	"mediacheck/internal/safeio"
)

func TestParseMPDHappyPath(t *testing.T) {
	prog := fixture.Generate(17, nil)
	if err := fixture.Materialize(prog); err != nil {
		t.Fatal(err)
	}
	m, err := ParseMPD(prog, safeio.DefaultLimits())
	if err != nil {
		t.Fatalf("ParseMPD: %v", err)
	}
	if len(m.Representations) != 5 {
		t.Fatalf("representations: %d", len(m.Representations))
	}
	for _, r := range m.Representations {
		dev := r.DeviationPct
		if dev < 0 {
			dev = -dev
		}
		if dev > 5.0 {
			t.Errorf("%s deviation %.3f%% exceeds 5%% (target %d actual %d)",
				r.ID, r.DeviationPct, r.TargetBitrate, r.ActualBitrate)
		}
		if len(r.Segments) != 8 {
			t.Errorf("%s segment count %d", r.ID, len(r.Segments))
		}
		if r.Init.Timescale != r.Timescale {
			t.Errorf("%s init timescale mismatch", r.ID)
		}
	}
	// Audio has no sidx: minimal locatable interval must be reported.
	for _, r := range m.Representations {
		if r.Kind != "audio" {
			continue
		}
		for _, s := range r.Segments {
			if !s.LocatableOnly {
				t.Fatalf("audio segment %s/%d should be locatable-only", r.ID, s.Sequence)
			}
		}
	}
}

func TestParseMPDTruncation(t *testing.T) {
	prog := fixture.Generate(17, []model.FaultKind{model.FaultTruncate})
	if err := fixture.Materialize(prog); err != nil {
		t.Fatal(err)
	}
	m, err := ParseMPD(prog, safeio.DefaultLimits())
	if err != nil {
		t.Fatalf("truncation must be classified, not fatal: %v", err)
	}
	var found int
	for _, r := range m.Representations {
		for _, s := range r.Segments {
			if s.Truncated {
				found++
			}
		}
	}
	if found == 0 {
		t.Fatal("no truncated segment located")
	}
}

func TestParseMPDZeroTimescale(t *testing.T) {
	prog := fixture.Generate(17, nil)
	prog.Video[0].Timescale = 0
	var se *model.StageError
	_, err := ParseMPD(prog, safeio.DefaultLimits())
	if !errors.As(err, &se) || !errors.Is(err, model.ErrZeroTimescale) {
		t.Fatalf("want zero-timescale stage error, got %v", err)
	}
}

func TestParseMPDLossyTimestamp(t *testing.T) {
	prog := fixture.Generate(17, nil)
	if err := fixture.Materialize(prog); err != nil {
		t.Fatal(err)
	}
	// 44100 -> 90000 cannot represent one unit losslessly.
	prog.Audio[0].Timescale = 44100
	prog.Audio[0].Segments[0].Duration = 1
	_, err := ParseMPD(prog, safeio.DefaultLimits())
	if !errors.Is(err, model.ErrLossyTime) {
		t.Fatalf("want lossy-time error, got %v", err)
	}
}

func TestParseMPDDurationBounds(t *testing.T) {
	prog := fixture.Generate(17, nil)
	if err := fixture.Materialize(prog); err != nil {
		t.Fatal(err)
	}
	lim := safeio.DefaultLimits()
	lim.MaxDurationTick = 10
	_, err := ParseMPD(prog, lim)
	if !errors.Is(err, model.ErrDurationBounds) {
		t.Fatalf("want duration-bounds error, got %v", err)
	}
}

func TestParseMPDDuplicateAndLate(t *testing.T) {
	prog := fixture.Generate(17, []model.FaultKind{
		model.FaultLate, model.FaultDuplicate,
	})
	if err := fixture.Materialize(prog); err != nil {
		t.Fatal(err)
	}
	m, err := ParseMPD(prog, safeio.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	var late, dup int
	for _, r := range m.Representations {
		for _, s := range r.Segments {
			if s.Late {
				late++
			}
			if s.Duplicate {
				dup++
			}
		}
	}
	if late == 0 || dup == 0 {
		t.Fatalf("late=%d dup=%d, want both > 0", late, dup)
	}
}
