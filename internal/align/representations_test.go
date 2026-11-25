package align

import (
	"fmt"
	"path/filepath"
	"testing"

	"mediapipeline/internal/fixture"
	"mediapipeline/internal/scan"
)

func buildInputs(t *testing.T, c fixture.Case, reps, frags int) []Input {
	t.Helper()
	cfg := fixture.Config{
		Root:            filepath.Join(t.TempDir(), "gen"),
		Case:            c,
		Representations: reps,
		Fragments:       frags,
	}
	descs, err := fixture.Generate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	inputs := make([]Input, 0, len(descs))
	for _, rep := range descs {
		init, err := scan.ReadInitSegment(filepath.Join(rep.Directory, "init.mp4"))
		if err != nil {
			t.Fatal(err)
		}
		tr := scan.NewSequenceTrackerWithDuration(rep.ID, rep.TickDuration)
		names := fragmentNames(t, rep.Directory)
		for i, name := range names {
			f, err := scan.ReadFragment(i, rep.ID, filepath.Join(rep.Directory, name))
			if err != nil {
				t.Fatalf("fragment %s: %v", name, err)
			}
			if err := tr.Observe(f); err != nil {
				t.Fatalf("observe %s: %v", name, err)
			}
		}
		inputs = append(inputs, Input{
			Representation: rep.ID,
			Timescale:      init.Timescale,
			Report:         tr.Report(),
		})
	}
	return inputs
}

func TestAlignNormalTwoRepresentations(t *testing.T) {
	inputs := buildInputs(t, fixture.CaseNormal, 2, 5)
	report, err := Align(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if report.RepresentationCount != 2 {
		t.Fatalf("representation count = %d", report.RepresentationCount)
	}
	// Low-rate timeline is 48 kHz: one tick rounds to 20833 ns and is the
	// coarsest (longest) tick, hence the alignment tolerance.
	if report.ToleranceNanos != 20833 {
		t.Fatalf("tolerance = %d, want 20833", report.ToleranceNanos)
	}
	if report.AlignedSegmentCount() != 5 {
		t.Fatalf("aligned segments = %d, want 5", report.AlignedSegmentCount())
	}
	if len(report.Misaligned) != 0 {
		t.Fatalf("healthy input must not be misaligned: %v", report.Misaligned)
	}
	b0 := report.Boundaries[0]
	if len(b0.Members) != 2 || !b0.Aligned {
		t.Fatalf("first boundary wrong: %+v", b0)
	}
	// 48 kHz and 90 kHz both start at zero; later boundaries must still be
	// within one coarse tick despite different timescales.
	for _, b := range report.Boundaries {
		var starts []int64
		for _, m := range b.Members {
			starts = append(starts, m.StartNanos)
		}
		if spread(starts) > report.ToleranceNanos {
			t.Fatalf("boundary %d spread exceeds tolerance: %v", b.Index, starts)
		}
	}
}

func TestAlignRegressionIsolated(t *testing.T) {
	inputs := buildInputs(t, fixture.CaseTFDTRegression, 2, 5)
	report, err := Align(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Regressions) != 2 {
		// One per representation.
		t.Fatalf("regressions = %d, want 2", len(report.Regressions))
	}
	// Four healthy fragments per rep remain; the regressed mfhd 3 must not
	// appear in any boundary member.
	for _, b := range report.Boundaries {
		for _, m := range b.Members {
			if m.MFHD == fixture.RegressionSeqForTest() {
				t.Fatalf("regressed mfhd leaked into boundary %d", b.Index)
			}
		}
	}
	if report.AlignedSegmentCount() != 4 {
		t.Fatalf("aligned segments = %d, want 4", report.AlignedSegmentCount())
	}
}

func TestAlignGapKeepsWindowsButRecordsGaps(t *testing.T) {
	inputs := buildInputs(t, fixture.CaseMfhdGap, 2, 5)
	report, err := Align(inputs)
	if err != nil {
		t.Fatal(err)
	}
	// Same-number hole on every representation: surviving windows stay
	// complete, but each scan report must still carry the explicit gap.
	for _, in := range inputs {
		if len(in.Report.Gaps) != 1 {
			t.Fatalf("%s should record one mfhd gap, got %+v", in.Representation, in.Report.Gaps)
		}
	}
	if report.AlignedSegmentCount() != 4 {
		t.Fatalf("aligned segments = %d, want 4", report.AlignedSegmentCount())
	}
}

func TestAlignSingleRepresentationHoleIsMisaligned(t *testing.T) {
	// Synthetic inputs: rep0 has three fragments, rep1 misses the middle
	// time window, so one boundary cannot cover every representation.
	inputs := []Input{
		syntheticInput("rep0", 90000, []uint64{0, 9000, 18000}),
		syntheticInput("rep1", 48000, []uint64{0, 9600}),
	}
	report, err := Align(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Misaligned) == 0 {
		t.Fatal("window missing rep1 must be marked misaligned")
	}
	if report.AlignedSegmentCount() != 2 {
		t.Fatalf("aligned segments = %d, want 2", report.AlignedSegmentCount())
	}
}

func TestAlignToleranceBoundary(t *testing.T) {
	// Coarse timeline 1000 Hz => one tick = 1,000,000 ns tolerance.
	inputs := []Input{
		syntheticInput("rep0", 1000, []uint64{0, 1000}),
		syntheticInput("rep1", 1000, []uint64{0, 1001}),
	}
	report, err := Align(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if report.ToleranceNanos != 1_000_000 {
		t.Fatalf("tolerance = %d", report.ToleranceNanos)
	}
	// 1 ms difference is exactly one coarse tick and must be accepted.
	if report.AlignedSegmentCount() != 2 {
		t.Fatalf("one-tick drift should align, got %+v", report.Misaligned)
	}

	drifted := []Input{
		syntheticInput("rep0", 1000, []uint64{0, 1000}),
		syntheticInput("rep1", 1000, []uint64{0, 1002}),
	}
	report2, err := Align(drifted)
	if err != nil {
		t.Fatal(err)
	}
	if report2.AlignedSegmentCount() != 1 {
		t.Fatal("two-tick drift must leave one misaligned window")
	}
}

func syntheticInput(repID string, timescale uint32, tfdt []uint64) Input {
	tr := scan.NewSequenceTrackerWithDuration(repID, uint64(timescale)/10)
	for i, base := range tfdt {
		f := &scan.Fragment{
			InputIndex: i,
			FileName:    fixture.FragmentName(uint32(i + 1)),
			InputPath:   repID + "/" + fixture.FragmentName(uint32(i+1)),
			SHA256:      fmt.Sprintf("sha-%s-%d", repID, i),
			MFHD:        uint32(i + 1),
			TFDT:        base,
		}
		if err := tr.Observe(f); err != nil {
			panic(err)
		}
	}
	return Input{Representation: repID, Timescale: timescale, Report: tr.Report()}
}

func TestAlignRequiresTwoRepresentations(t *testing.T) {
	if _, err := Align(nil); err == nil {
		t.Fatal("nil inputs must be rejected")
	}
}

func spread(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	min, max := values[0], values[0]
	for _, v := range values {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	return max - min
}
