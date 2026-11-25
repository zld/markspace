package scan

import (
	"fmt"

	"mediapipeline/internal/fault"
)

// Gap records an explicit missing mfhd sequence number.
type Gap struct {
	MissingMFHD uint32 `json:"missing_mfhd"`
	// AfterMFHD is the last observed sequence before the hole (0 when the
	// very first fragment is missing).
	AfterMFHD uint32 `json:"after_mfhd"`
}

// Regression records one tfdt backwards movement. Original source values are
// preserved; the timeline is never rewritten.
type Regression struct {
	InputPath    string `json:"input_path"`
	MFHD         uint32 `json:"mfhd"`
	PreviousTFDT uint64 `json:"previous_tfdt"`
	ObservedTFDT uint64 `json:"observed_tfdt"`
	InputSHA256  string `json:"input_sha256"`
}

// TrackReport is the per-representation result of sequence analysis.
type TrackReport struct {
	Representation string
	// TickDuration is the constant fragment duration in timescale ticks.
	TickDuration uint64
	// Fragments holds only the accepted (folded, gap-free) records in
	// mfhd order. Regressed fragments are isolated, not included here.
	Fragments   []*Fragment
	Gaps        []Gap
	Regressions []Regression
	// Isolated lists fragments excluded from alignment/manifest because of
	// a tfdt regression, preserving their original records.
	Isolated []*Fragment
}

// FragmentDuration returns the duration in ticks of the fragment carrying
// mfhd. The generator produces constant-duration fragments.
func (r *TrackReport) FragmentDuration(mfhd uint32) uint64 {
	return r.TickDuration
}

// SequenceTracker consumes one representation's fragments in input-index
// order and enforces mfhd continuity, duplicate rules and monotonic tfdt.
type SequenceTracker struct {
	representation string
	lastMFHD       uint32
	lastTFDT       uint64
	haveLast       bool
	byMFHD         map[uint32]*Fragment
	report         *TrackReport
}

// NewSequenceTracker starts a tracker for one representation.
func NewSequenceTracker(representation string) *SequenceTracker {
	return NewSequenceTrackerWithDuration(representation, 0)
}

// NewSequenceTrackerWithDuration starts a tracker and records the constant
// fragment duration in ticks, needed later for nanosecond end-time alignment.
func NewSequenceTrackerWithDuration(representation string, tickDuration uint64) *SequenceTracker {
	return &SequenceTracker{
		representation: representation,
		byMFHD:         map[uint32]*Fragment{},
		report:         &TrackReport{Representation: representation, TickDuration: tickDuration},
	}
}

// Observe feeds one validated fragment. A duplicate conflict stops the group:
// the returned error is fault.CodeDuplicateConflict and carries the differing
// fields. Gaps and regressions are recorded but do not stop processing.
func (t *SequenceTracker) Observe(f *Fragment) error {
	if existing, ok := t.byMFHD[f.MFHD]; ok {
		if existing.SHA256 == f.SHA256 && existing.TFDT == f.TFDT {
			// Identical digest and time range: fold the duplicate silently.
			return nil
		}
		return fault.New(fault.CodeDuplicateConflict,
			"mfhd %d repeated with different content", f.MFHD).
			WithInput(f.InputPath).
			WithDetail("first_input", existing.InputPath).
			WithDetail("duplicate_input", f.InputPath).
			WithDetail("first_sha256", existing.SHA256).
			WithDetail("duplicate_sha256", f.SHA256).
			WithDetail("first_tfdt", existing.TFDT).
			WithDetail("duplicate_tfdt", f.TFDT)
	}

	if t.haveLast && f.MFHD <= t.lastMFHD {
		// Out-of-order arrival below the high-water mark but not an exact
		// duplicate is also a conflict, since its number was never seen.
		return fault.New(fault.CodeDuplicateConflict,
			"mfhd %d arrived after high-water mark %d", f.MFHD, t.lastMFHD).
			WithInput(f.InputPath)
	}
	if t.haveLast {
		for want := t.lastMFHD + 1; want < f.MFHD; want++ {
			t.report.Gaps = append(t.report.Gaps, Gap{MissingMFHD: want, AfterMFHD: t.lastMFHD})
		}
	}

	if t.haveLast && f.TFDT < t.lastTFDT {
		t.report.Regressions = append(t.report.Regressions, Regression{
			InputPath:    f.InputPath,
			MFHD:         f.MFHD,
			PreviousTFDT: t.lastTFDT,
			ObservedTFDT: f.TFDT,
			InputSHA256:  f.SHA256,
		})
		t.report.Isolated = append(t.report.Isolated, f)
		// Isolated fragments do not become the new high-water reference for
		// monotonic time, but do occupy their sequence number.
		t.byMFHD[f.MFHD] = f
		t.lastMFHD = f.MFHD
		return nil
	}

	t.byMFHD[f.MFHD] = f
	t.report.Fragments = append(t.report.Fragments, f)
	t.lastMFHD = f.MFHD
	t.lastTFDT = f.TFDT
	t.haveLast = true
	return nil
}

// Report finalizes and returns the analysis result.
func (t *SequenceTracker) Report() *TrackReport {
	return t.report
}

// String renders a compact human-readable summary for diagnostics.
func (r *TrackReport) String() string {
	return fmt.Sprintf("%s: fragments=%d gaps=%d regressions=%d isolated=%d",
		r.Representation, len(r.Fragments), len(r.Gaps), len(r.Regressions), len(r.Isolated))
}
