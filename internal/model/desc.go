package model

import "fmt"

// BoundaryType enumerates the discontinuities a fixture injects at a clip
// boundary. It is distinct from repair Kind: the descriptor declares intent,
// the repair subsystem independently re-detects it from the raw stream.
type BoundaryType string

const (
	// BNone: a clean clip boundary, no discontinuity.
	BNone BoundaryType = "none"
	// BWrap: inject a 32-bit RTP clock wraparound.
	BWrap BoundaryType = "wrap"
	// BSwitch: inject a source switch (clock and source id change).
	BSwitch BoundaryType = "switch"
	// BIllegal: inject an undecidable backward jump, not repairable.
	BIllegal BoundaryType = "illegal"
	// BTruncate: mark the trailing packet block as truncated at generation.
	BTruncate BoundaryType = "truncate"
)

// BoundarySpec declares one injected boundary after sample AfterSeq.
type BoundarySpec struct {
	AfterSeq int          `json:"after_seq"`
	Type     BoundaryType `json:"type"`
	// ExpectedNS anchors the repaired fixed time of the right-hand sample for
	// source-switch boundaries (the raw clock alone cannot derive it).
	ExpectedNS TimeNS `json:"expected_ns,omitempty"`
}

// TrackDesc fully describes one generated track and its timeline.
type TrackDesc struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	ClockRate uint32 `json:"clock_rate"`
	Samples   int    `json:"samples"`
	// StepTicks is the constant per-sample RTP clock increment.
	StepTicks  uint32         `json:"step_ticks"`
	StartTS    uint32         `json:"start_ts"`
	Boundaries []BoundarySpec `json:"boundaries"`
}

// CaseDesc is the on-disk fixture descriptor, stored inside the MP4 avpi box.
type CaseDesc struct {
	Name    string      `json:"name"`
	Damaged bool        `json:"damaged"`
	Tracks  []TrackDesc `json:"tracks"`
}

// RTPFile/MP4 file names for a given track id.
const (
	// IndexMP4 is the MP4 container holding the descriptor and time boxes.
	IndexMP4 = "index.mp4"
	// AVPIBox is the custom box type carrying the JSON descriptor.
	AVPIBox = "avpi"
)

// RTPPath returns the generated RTP packet-file name for a track.
func RTPPath(trackID int) string {
	return fmtTrack(trackID) + ".rtp"
}

func fmtTrack(id int) string {
	return fmt.Sprintf("track%02d", id)
}
