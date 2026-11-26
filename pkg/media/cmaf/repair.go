package cmaf

import (
	"sort"

	"cmafrepair/pkg/media/timebase"
)

// RepairStatus classifies the outcome for one fragment.
type RepairStatus string

// Repair outcomes.
const (
	StatusRepaired  RepairStatus = "REPAIRED"  // tfdt corrected or confirmed, fragment usable
	StatusIntact    RepairStatus = "INTACT"    // original tfdt already matched the rebuilt timeline
	StatusGap       RepairStatus = "GAP"       // duration cannot be inferred; no time fabricated
	StatusTruncated RepairStatus = "TRUNCATED" // mdat bytes missing; fragment not independently decodable
)

// TimelineEntry is the repaired record for one fragment of one representation.
type TimelineEntry struct {
	Name         string
	Sequence     uint32
	Status       RepairStatus
	DeclaredTfdt uint64 // value found on disk
	FixedTfdt    uint64 // rebuilt base media decode time (0 when GAP)
	SampleCount  int
	Duration     uint64 // sum of sample durations in track timescale (0 when GAP)
	StartMs      int64  // shared-clock start, rounded; 0 when GAP
	EndMs        int64  // shared-clock end; equals next entry StartMs when contiguous
	SyncCount    int    // samples flagged as random-access points
	Defects      []*timebase.MediaError
}

func (e *TimelineEntry) addDefect(d *timebase.MediaError) {
	if d != nil {
		e.Defects = append(e.Defects, d)
	}
}

// Timeline is the repaired, monotonic view of one representation.
type Timeline struct {
	Representation string
	Timescale      uint32
	Entries        []TimelineEntry
	// GapCount counts GAP entries; TruncatedCount counts mdat-truncated ones.
	GapCount       int
	TruncatedCount int
	// RepairedCount counts entries whose on-disk tfdt was changed.
	RepairedCount int
}

// TimelineRepair rebuilds the tfdt timeline of a representation purely from
// trun/tfhd durations. It never invents time: when a fragment's durations
// cannot be determined, the fragment is marked GAP and the expected clock is
// carried forward without guessing how long the gap lasted.
type TimelineRepair struct {
	rep       string
	timescale uint32
	// trex/tfhd defaults applied when trun lacks per-sample durations.
	defaultDuration uint32
	expectedSeq     uint32
	haveExpected    bool
	expectedTime    uint64
	localRepaired   int
	gapCount        int
	truncatedCount  int
	entries         []TimelineEntry
}

// NewTimelineRepair starts a repair session for one representation.
func NewTimelineRepair(rep string, init *InitInfo) *TimelineRepair {
	tr := &TimelineRepair{rep: rep}
	if init != nil {
		tr.timescale = init.Timescale
		tr.defaultDuration = init.TrexDefaultDuration
	}
	return tr
}

// Add feeds one parsed fragment, already associated with this representation.
// It returns the repaired entry. The fragment's Samples get their absolute
// MediaTime assigned and tfdt fix anchor validated.
func (t *TimelineRepair) Add(f *Fragment) (TimelineEntry, error) {
	if t.timescale == 0 {
		return TimelineEntry{}, timebase.NewError(timebase.CodeInputInvalid, timebase.PhaseRepair,
			"representation %s has no timescale", t.rep)
	}
	entry := TimelineEntry{
		Name:         f.Name,
		Sequence:     f.Sequence,
		DeclaredTfdt: f.Tfdt,
		SampleCount:  len(f.Samples),
		Defects:      append([]*timebase.MediaError{}, f.Defects...),
	}

	// Detect mfhd sequence gaps against the expected next number.
	if t.haveExpected && f.Sequence != t.expectedSeq {
		entry.addDefect(timebase.AtError(timebase.CodeMfhdGap, timebase.PhaseValidate,
			f.SeqOffset, "sequence jumped from %d to %d", t.expectedSeq, f.Sequence).
			WithContext(t.rep, f.Name).WithBox(BoxMfhd))
	}
	// A duplicate sequence number is always a hard defect.
	for _, prior := range t.entries {
		if prior.Sequence == f.Sequence {
			entry.addDefect(timebase.AtError(timebase.CodeMfhdDuplicate, timebase.PhaseValidate,
				f.SeqOffset, "duplicate sequence number %d", f.Sequence).
				WithContext(t.rep, f.Name).WithBox(BoxMfhd))
		}
	}

	duration, inferable := t.fragmentDuration(f)
	if !inferable {
		// Cannot determine duration: mark GAP and do NOT fabricate time.
		entry.Status = StatusGap
		entry.FixedTfdt = t.expectedTime
		t.GapCarry(&entry)
		t.entries = append(t.entries, entry)
		t.advance(f.Sequence, 0)
		return entry, nil
	}

	entry.Duration = duration
	entry.FixedTfdt = t.expectedTime
	entry.SampleCount = len(f.Samples)

	// Assign each sample its absolute media time and sync count.
	cursor := t.expectedTime
	for i := range f.Samples {
		f.Samples[i].MediaTime = cursor
		if f.Samples[i].Sync {
			entry.SyncCount++
		}
		cursor += uint64(f.Samples[i].Duration)
	}

	// Detect tfdt rollback relative to the rebuilt monotonic timeline.
	if f.Tfdt < t.expectedTime && t.haveExpected {
		entry.addDefect(timebase.AtError(timebase.CodeTfdtRollback, timebase.PhaseValidate,
			f.TfdtOffset, "tfdt %d precedes expected %d", f.Tfdt, t.expectedTime).
			WithContext(t.rep, f.Name).WithBox(BoxTfdt))
	}
	if f.Tfdt != t.expectedTime {
		entry.Status = StatusRepaired
		t.localRepaired++
	} else {
		entry.Status = StatusIntact
	}
	if !f.MdatOK {
		entry.Status = StatusTruncated
		t.truncatedCount++
	}

	// Rational mapping to the shared millisecond clock.
	startMs, err := timebase.ToSharedMs(t.expectedTime, t.timescale)
	if err != nil {
		return entry, err
	}
	endMs, err := timebase.ToSharedMs(t.expectedTime+duration, t.timescale)
	if err != nil {
		return entry, err
	}
	entry.StartMs, entry.EndMs = startMs, endMs

	t.entries = append(t.entries, entry)
	t.advance(f.Sequence, duration)
	return entry, nil
}

// fragmentDuration sums sample durations, applying tfhd/trex defaults only
// when trun omits per-sample durations. Returns inferable=false if any sample
// duration is genuinely unknown.
func (t *TimelineRepair) fragmentDuration(f *Fragment) (uint64, bool) {
	if len(f.Samples) == 0 {
		return 0, false
	}
	trunHasDur := f.TrunFlags&0x000100 != 0
	var total uint64
	for i := range f.Samples {
		d := f.Samples[i].Duration
		if !trunHasDur || d == 0 {
			if t.defaultDuration == 0 {
				return 0, false
			}
			d = t.defaultDuration
			f.Samples[i].Duration = d
		}
		total += uint64(d)
	}
	return total, true
}

func (t *TimelineRepair) advance(seq uint32, duration uint64) {
	t.expectedSeq = seq + 1
	t.haveExpected = true
	t.expectedTime += duration
}

// GapCarry fills shared-clock fields on a GAP entry without inventing time.
func (t *TimelineRepair) GapCarry(e *TimelineEntry) {
	t.gapCount++
	if ms, err := timebase.ToSharedMs(t.expectedTime, t.timescale); err == nil {
		e.StartMs, e.EndMs = ms, ms
	}
}

// Build finalizes and returns the repaired timeline.
func (t *TimelineRepair) Build() *Timeline {
	out := &Timeline{
		Representation: t.rep,
		Timescale:      t.timescale,
		Entries:        append([]TimelineEntry{}, t.entries...),
		GapCount:       t.gapCount,
		TruncatedCount: t.truncatedCount,
		RepairedCount:  t.localRepaired,
	}
	sort.SliceStable(out.Entries, func(i, j int) bool {
		return out.Entries[i].Sequence < out.Entries[j].Sequence
	})
	return out
}
