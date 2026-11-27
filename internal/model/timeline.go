package model

// EntryClass distinguishes real decoded samples from bookkeeping records.
type EntryClass string

const (
	// EntrySample is a packet whose payload contributes real media time.
	EntrySample EntryClass = "sample"
	// EntryDuplicate is a repeated sequence number: it is recorded but adds
	// zero duration and zero bytes to the timeline.
	EntryDuplicate EntryClass = "duplicate"
	// EntryGap marks a missing sequence range. The slot is preserved so the
	// loss is visible; no audio bytes are fabricated for it.
	EntryGap EntryClass = "gap"
)

// TimelineEntry is one explicit point on a track's unified timeline.
//
// Start/Duration are integer nanoseconds on the shared time base. TimeDelta
// is the difference between this entry's placed start and the start a lossless
// ordered stream would have had; ByteDelta is the same comparison for the
// cumulative accounted payload byte count. Both are zero on a clean stream.
type TimelineEntry struct {
	Class       EntryClass `json:"class"`
	SourceIndex int        `json:"source_index"` // index into TrackInput.Packets
	ExtSeq      uint64     `json:"ext_seq"`      // unwrapped sequence number
	Start       NS         `json:"start_ns"`
	Duration    NS         `json:"duration_ns"`
	TimeDelta   NS         `json:"time_delta_ns"`
	Bytes       int        `json:"payload_bytes"`
	ByteDelta   int        `json:"byte_delta"`
	Failure     *Failure   `json:"failure,omitempty"`
}

// TrackTimeline is the ordered timeline of one track plus its audit trail.
// Entries are sorted by (Start, ExtSeq, SourceIndex) so the order is stable
// across reruns and independent of map iteration.
type TrackTimeline struct {
	TrackID string          `json:"track_id"`
	Entries []TimelineEntry `json:"entries"`
	// Failures holds every parse/validation failure seen on this track. A
	// track may therefore have entries and failures at the same time.
	Failures []Failure `json:"failures"`
	// FirstExtSeq/LastExtSeq describe the inclusive extended sequence range
	// the track occupies, including missing numbers.
	FirstExtSeq uint64 `json:"first_ext_seq"`
	LastExtSeq  uint64 `json:"last_ext_seq"`
}

// TotalDuration returns the media duration represented by real samples and
// gaps alike, i.e. the span from 0 to the end of the last entry.
func (t *TrackTimeline) TotalDuration() NS {
	var end NS
	for i := range t.Entries {
		if e := t.Entries[i].Start + t.Entries[i].Duration; e > end {
			end = e
		}
	}
	return end
}

// SampleBytes sums the payload bytes of real samples (duplicates excluded).
func (t *TrackTimeline) SampleBytes() int {
	var n int
	for i := range t.Entries {
		if t.Entries[i].Class == EntrySample {
			n += t.Entries[i].Bytes
		}
	}
	return n
}

// HasFailure reports whether the track recorded any failure.
func (t *TrackTimeline) HasFailure() bool { return len(t.Failures) > 0 }
