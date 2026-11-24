package model

// Packet is the common media event exchanged by stream, clock, segment,
// audio, fingerprint and pipeline packages.
type Packet struct {
	SourceOffset  int64  `json:"source_offset"`
	RecordIndex   int    `json:"record_index"`
	Sequence      uint16 `json:"sequence"`
	ExtSequence   uint64 `json:"ext_sequence"`
	Timestamp     uint32 `json:"timestamp"`
	ExtTimestamp  uint64 `json:"ext_timestamp"`
	SSRC          uint32 `json:"ssrc"`
	Marker        bool   `json:"marker"`
	Payload       []byte `json:"-"`
	Raw           []byte `json:"-"`
	MediaMs       int64  `json:"media_ms"`
	FrameMs       int64  `json:"frame_ms"`
	PayloadValid  bool   `json:"payload_valid"`
	PayloadReason string `json:"payload_reason,omitempty"`
}

type Segment struct {
	Index               int     `json:"index"`
	StartMs             int64   `json:"start_ms"`
	EndMs               int64   `json:"end_ms"`
	PacketCount         int     `json:"packet_count"`
	PayloadBytes        int64   `json:"payload_bytes"`
	BitrateBps          float64 `json:"bitrate_bps"`
	Complete            bool    `json:"complete"`
	Recoverable         bool    `json:"recoverable"`
	StartExtSequence    uint64  `json:"start_ext_sequence"`
	EndExtSequence      uint64  `json:"end_ext_sequence"`
	StartAlignmentMs    int64   `json:"start_alignment_error_ms,omitempty"`
	BoundaryAlignmentMs int64   `json:"boundary_alignment_error_ms,omitempty"`
	MergedInto          *int    `json:"merged_into,omitempty"`
}

type WindowState string

const (
	WindowSilent  WindowState = "silent"
	WindowVoiced  WindowState = "voiced"
	WindowUnknown WindowState = "unknown"
)

type SilenceWindow struct {
	SegmentIndex int         `json:"segment_index"`
	WindowIndex  int         `json:"window_index"`
	StartMs      int64       `json:"start_ms"`
	EndMs        int64       `json:"end_ms"`
	State        WindowState `json:"state"`
	Amplitude    float64     `json:"amplitude,omitempty"`
	Reason       string      `json:"reason,omitempty"`
}

type WindowFingerprint struct {
	SegmentIndex int    `json:"segment_index"`
	WindowIndex  int    `json:"window_index"`
	StartMs      int64  `json:"start_ms"`
	EndMs        int64  `json:"end_ms"`
	Hash         uint64 `json:"hash"`
	SampleCount  int    `json:"sample_count"`
	Complete     bool   `json:"complete"`
}

type DedupeMapping struct {
	KeptSegment     int   `json:"kept_segment"`
	MergedSegment   int   `json:"merged_segment"`
	KeptStartMs     int64 `json:"kept_start_ms"`
	MergedStartMs   int64 `json:"merged_start_ms"`
	StartDeltaMs    int64 `json:"start_delta_ms"`
	DurationDeltaMs int64 `json:"duration_delta_ms"`
	MatchingWindows int   `json:"matching_windows"`
	TotalWindows    int   `json:"total_windows"`
	TimeToleranceMs int64 `json:"time_tolerance_ms"`
}
