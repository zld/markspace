package model

// Repair records one timestamp/source continuity correction. Orig and Fixed
// retain both the source timescale value (when known) and microsecond value.
type Repair struct {
	Kind        string `json:"kind"`
	Source      string `json:"source"`
	OldSource   string `json:"old_source,omitempty"`
	TrackID     uint32 `json:"track_id"`
	BoxType     string `json:"box_type,omitempty"`
	BoxOffset   int64  `json:"box_offset"`
	OrigRaw     int64  `json:"orig_raw"`
	FixedRaw    int64  `json:"fixed_raw"`
	OrigUs      Us     `json:"orig_us"`
	FixedUs     Us     `json:"fixed_us"`
	OffsetUs    Us     `json:"offset_us"`
	Timescale   uint32 `json:"timescale,omitempty"`
	Description string `json:"description,omitempty"`
}

// Gap is a missing mfhd sequence interval. MissingCount does not create any
// synthetic fragment duration.
type Gap struct {
	Source       string `json:"source"`
	TrackRole    string `json:"track_role,omitempty"`
	AfterSeq     uint32 `json:"after_seq"`
	BeforeSeq    uint32 `json:"before_seq"`
	MissingCount uint32 `json:"missing_count"`
	EventIndex   int    `json:"event_index"`
	BoxOffset    int64  `json:"box_offset,omitempty"`
}

// Quarantine identifies an unindexed truncated/out-of-bounds fragment.
type Quarantine struct {
	Source          string `json:"source"`
	File            string `json:"file"`
	BoxType         string `json:"box_type"`
	BoxOffset       int64  `json:"box_offset"`
	FirstUnreadable int64  `json:"first_unreadable_offset"`
	Reason          string `json:"reason"`
}
