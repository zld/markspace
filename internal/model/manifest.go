package model

// SegmentRef is the raw segment metadata as encoded in the input JSON.
// Times are integer ticks inside the representation's own Timescale.
type SegmentRef struct {
	Number       int64 `json:"number"`
	StartTick    int64 `json:"start_tick"`
	Duration     int64 `json:"duration_tick"`
	RandomAccess bool  `json:"random_access"`
}

// RepresentationInput is one encoded rend of the same edit.
type RepresentationInput struct {
	ID        string       `json:"id"`
	Bandwidth int64        `json:"bandwidth_bps"`
	Width     int64        `json:"width"`
	Height    int64        `json:"height"`
	Timescale int64        `json:"timescale"`
	Segments  []SegmentRef `json:"segments"`
}

// Description is the parsed input file.
type Description struct {
	ContentID       string                `json:"content_id"`
	Representations []RepresentationInput `json:"representations"`
}

// SegmentTimelineEntry is one <S> element. A present T marks a segment
// whose start cannot be derived by accumulation from the previous one.
type SegmentTimelineEntry struct {
	Number int64
	T      int64
	D      int64
	HasT   bool
}

// SegmentTemplate carries the static DASH template plus the fully
// expanded timeline. Time values live in the template's Timescale.
type SegmentTemplate struct {
	Timescale      int64
	StartNumber    int64
	Media          string
	Initialization string
	Timeline       []SegmentTimelineEntry
	// UsesTime is true when segment numbering is non-contiguous and the
	// media template therefore keys segments on $Time$ instead of $Number$.
	UsesTime bool
	// CoverageTick is the independently summed duration of all timeline
	// entries; MPD writing re-checks it against the representation range.
	CoverageTick int64
}

// Representation is one rend in the reusable manifest model.
type Representation struct {
	ID        string
	Bandwidth int64
	Width     int64
	Height    int64
	Timescale int64
	Template  SegmentTemplate
	// Start/End in unified seconds, derived from exact rational math.
	Start    Rat
	End      Rat
	Duration Rat
	// DeltaToLongestMs is the gap to the longest representation in
	// milliseconds, as an exact rational rendered by the report.
	DeltaToLongestMs Rat
}

// AdaptationSet groups the video representations of one Period.
type AdaptationSet struct {
	ID              int
	ContentType     string
	MimeType        string
	Representations []*Representation
}

// Period is the only supported container: single-Period, video only.
type Period struct {
	ID             int
	Start          Rat
	Duration       Rat
	AdaptationSets []*AdaptationSet
}

// SwitchPoint is a boundary accepted onto the common timeline.
type SwitchPoint struct {
	// Time is the common time in unified seconds.
	Time Rat
	// BoundaryTick records each representation's own-tick boundary that
	// mapped onto Time, keyed by representation ID.
	BoundaryTick map[string]int64
	// SkewMs is the largest pairwise skew at this point, in milliseconds.
	SkewMs Rat
}

// Rejection records an alignment failure with the original (never
// re-based) times preserved.
type Rejection struct {
	Reason         string
	Representation string
	Tick           int64
	Time           Rat
	SkewMs         Rat
	Detail         string
}

// Manifest is the single model consumed by both the MPD writer and the
// diagnostic report, so packaging and diagnostics always share one
// interpretation of the timeline.
type Manifest struct {
	ContentID                 string
	MediaPresentationDuration Rat
	Period                    *Period
	SwitchPoints              []SwitchPoint
	Rejections                []Rejection
	MaxBoundarySkewMs         Rat
	Tolerance                 Rat
	Loose                     bool
	// Alignable is false when strict publishing must be refused.
	Alignable bool
}
