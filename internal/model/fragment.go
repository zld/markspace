package model

import (
	"fmt"
	"sort"
)

// Track describes one media track declared by the init segment.
type Track struct {
	TrackID      uint32 `json:"track_id"`
	Timescale    uint32 `json:"timescale"`
	Handler      string `json:"handler"`
	DefaultDur   uint32 `json:"default_sample_duration"`
	DefaultSize  uint32 `json:"default_sample_size"`
	DefaultFlags uint32 `json:"default_sample_flags"`
	Codec        string `json:"codec,omitempty"`
}

// Sample is a single coded media sample described by a trun entry.
// Durations stay in track ticks; DecodeMicros holds the repaired unified
// decode time. SourceOffset is the absolute byte offset of the sample data in
// the source stream (the trun data offset resolved against default-base-is-moof).
type Sample struct {
	Duration     uint32 `json:"duration"`
	Size         uint32 `json:"size"`
	Flags        uint32 `json:"flags"`
	Composition  int32  `json:"composition_offset"`
	DecodeTicks  uint64 `json:"decode_ticks"`
	DecodeMicros Micros `json:"decode_us"`
	IsSync       bool   `json:"is_sync"`
	SourceOffset uint64 `json:"source_offset"`
}

// FragmentTrack is the per-track view of one traf inside a moof.
type FragmentTrack struct {
	TrackID         uint32   `json:"track_id"`
	Timescale       uint32   `json:"timescale"`
	Sequence        uint32   `json:"sequence"`
	SequenceKnown   bool     `json:"sequence_known"`
	TfdtPresent     bool     `json:"tfdt_present"`
	OriginalTfdt    uint64   `json:"original_tfdt_ticks"`
	Tfdt            uint64   `json:"tfdt_ticks"`
	TfdtVersion     uint8    `json:"tfdt_version"`
	TfdtPatchAt     uint64   `json:"-"`
	DefaultDuration uint32   `json:"default_sample_duration"`
	TfdtRegressed   bool     `json:"tfdt_regressed"`
	Repaired        bool     `json:"repaired"`
	DurationTicks   uint64   `json:"duration_ticks"`
	StartTicks      uint64   `json:"start_ticks"`
	EndTicks        uint64   `json:"end_ticks"`
	StartMicros     Micros   `json:"start_us"`
	EndMicros       Micros   `json:"end_us"`
	FirstOffset     uint64   `json:"first_sample_offset"`
	SampleBytes     uint64   `json:"sample_bytes"`
	Samples         []Sample `json:"samples,omitempty"`
}

// SyncSampleOrdinal returns the zero-based ordinal of the first sync sample,
// and whether any sync sample is present.
func (ft *FragmentTrack) SyncSampleOrdinal() (uint32, bool) {
	for i := range ft.Samples {
		if ft.Samples[i].IsSync {
			return uint32(i), true
		}
	}
	return 0, false
}

// NominalSampleDuration returns the duration of the first sample, falling
// back to the default duration of the track.
func (ft *FragmentTrack) NominalSampleDuration() uint32 {
	if len(ft.Samples) > 0 && ft.Samples[0].Duration > 0 {
		return ft.Samples[0].Duration
	}
	return 0
}

// Fragment is one moof/mdat pair along with one or more track payloads.
// Byte ranges refer to the source stream. Truncated and Discarded mark
// isolated damage; Recovered marks fragments read after a resynchronisation.
type Fragment struct {
	Index      int             `json:"index"`
	Range      ByteRange       `json:"range"`
	MoofRange  ByteRange       `json:"moof_range"`
	MdatRange  ByteRange       `json:"mdat_range"`
	HasMdat    bool            `json:"has_mdat"`
	Sequence   uint32          `json:"sequence"`
	Truncated  bool            `json:"truncated"`
	Discarded  bool            `json:"discarded"`
	Recovered  bool            `json:"recovered"`
	MFHDGap    int             `json:"mfhd_gap"`
	Duplicate  bool            `json:"duplicate_sequence"`
	Tracks     []FragmentTrack `json:"tracks"`
	Issues     []Issue         `json:"issues,omitempty"`
	ExtraBoxes []RawBox        `json:"-"`
	Moof       []byte          `json:"-"`
	Mdat       []byte          `json:"-"`
}

// SampleCount returns the total number of samples across tracks.
func (f *Fragment) SampleCount() int {
	n := 0
	for i := range f.Tracks {
		n += len(f.Tracks[i].Samples)
	}
	return n
}

// ValidSamples returns the number of samples that can be delivered.
func (f *Fragment) ValidSamples() int {
	if f.Discarded {
		return 0
	}
	return f.SampleCount()
}

// AddIssue appends an auditable anomaly.
func (f *Fragment) AddIssue(issue Issue) {
	f.Issues = append(f.Issues, issue)
}

// Index returns the fragment's position in the source stream.
func (f *Fragment) Order() int { return f.Index }

// RawBox is a preserved box with its original bytes. Unknown boxes travel
// through the pipeline unchanged and remain subject to boundary validation.
type RawBox struct {
	Type    string    `json:"type"`
	Range   ByteRange `json:"range"`
	Bytes   []byte    `json:"-"`
	Unknown bool      `json:"unknown,omitempty"`
}

// InitSegment is the parsed initialization data (ftyp plus moov and any
// other leading boxes).
type InitSegment struct {
	Bytes  []byte   `json:"-"`
	Boxes  []RawBox `json:"boxes,omitempty"`
	Tracks []Track  `json:"tracks"`
}

// PreservedBox is a non-fragment box carried through to the delivered file so
// unknown boxes are not silently dropped. Bytes holds the complete box.
type PreservedBox struct {
	Type  string    `json:"type"`
	Range ByteRange `json:"range"`
	Bytes []byte    `json:"-"`
}

// SourceSet is everything read from one input stream.
type SourceSet struct {
	Name          string       `json:"name"`
	InitRange     ByteRange    `json:"init_range"`
	InitBytes     uint64       `json:"init_bytes"`
	Init          *InitSegment `json:"-"`
	Tracks        []Track      `json:"tracks"`
	Fragments     []Fragment   `json:"fragments"`
	TrailingBytes uint64       `json:"trailing_bytes"`
	DroppedBytes  uint64       `json:"dropped_bytes"`
	DamagedIndex  bool         `json:"damaged_index"`
	IndexIssues   []Issue      `json:"index_issues,omitempty"`
	Issues        []Issue      `json:"issues,omitempty"`
	// Preserved boxes appear after the init segment and before the first moof;
	// they are delivered verbatim so unknown boxes survive the repair.
	Preserved []PreservedBox `json:"preserved,omitempty"`
}

// TrackByID returns the track with the given ID.
func (s *SourceSet) TrackByID(id uint32) (Track, bool) {
	for _, t := range s.Tracks {
		if t.TrackID == id {
			return t, true
		}
	}
	return Track{}, false
}

// SortedTracks returns tracks ordered by ID for deterministic output.
func (s *SourceSet) SortedTracks() []Track {
	out := append([]Track(nil), s.Tracks...)
	sort.Slice(out, func(i, j int) bool { return out[i].TrackID < out[j].TrackID })
	return out
}

// DeliveredFragments returns fragments that survived isolation, in order.
func (s *SourceSet) DeliveredFragments() []Fragment {
	out := make([]Fragment, 0, len(s.Fragments))
	for _, f := range s.Fragments {
		if f.Discarded || f.Truncated {
			continue
		}
		out = append(out, f)
	}
	return out
}

// Validate performs the checks that gate a successful manifest: the init
// segment must exist and every declared track must have a nonzero timescale.
func (s *SourceSet) Validate() error {
	if s.Init == nil || len(s.Init.Bytes) == 0 || len(s.Tracks) == 0 {
		return NewTaskError(CategoryMissingInit, "ingest", "init segment with moov/trak is missing")
	}
	for _, t := range s.Tracks {
		if t.Timescale == 0 {
			return NewTaskError(CategoryZeroTimescale, "ingest",
				fmt.Sprintf("track %d declares timescale 0", t.TrackID)).WithTrack(t.TrackID)
		}
	}
	return nil
}
