// Package scan demuxes local capture assets and extracts timed metadata:
// MP4 tracks/fragments, emsg, WebVTT, chapter marks, ID3 tags and RTP-carried
// H.264/H.265 SEI. Every output keeps its original time base and byte offset.
package scan

import "mediad/internal/model"

// Track kind constants. They are stable identifiers used in manifests.
const (
	KindVideo    = "video"
	KindAudio    = "audio"
	KindSubtitle = "subtitle"
	KindChapter  = "chapter"
	KindMetadata = "metadata"
	KindSEI      = "sei"
	KindID3      = "id3"
	KindEmsg     = "emsg"
)

// Event kind constants for TimedEvent.Kind.
const (
	EventEmsg    = "emsg"
	EventSEI     = "sei"
	EventID3     = "id3"
	EventCue     = "cue"
	EventChapter = "chapter"
)

// TrackSummary describes one parsed track with its exact time base.
type TrackSummary struct {
	ID          uint32         `json:"id"`
	Kind        string         `json:"kind"`
	Handler     string         `json:"handler"`
	TimeBase    model.Rational `json:"time_base"`
	Duration    int64          `json:"duration_ticks"`
	SampleCount int            `json:"sample_count"`
	Fragmented  bool           `json:"fragmented"`
	Codec       string         `json:"codec"`
}

// Sample is one media sample (or fragment sample) with source byte range and
// its original timestamp tick.
type Sample struct {
	TrackID uint32 `json:"track_id"`
	Tick    int64  `json:"tick"`     // decode time in the track time base
	DurTick int64  `json:"dur_tick"` // duration in the track time base
	Offset  int64  `json:"offset"`   // absolute byte offset in the source
	Length  int64  `json:"length"`   // byte length
	IsSync  bool   `json:"is_sync"`  // recovery boundary candidate
}

// TimedEvent is a metadata event (emsg/SEI/ID3/cue/chapter). Ordering
// anomalies preserve the raw tick, offset and payload — never rewritten.
type TimedEvent struct {
	TrackID  uint32         `json:"track_id"`
	Kind     string         `json:"kind"`
	Tick     int64          `json:"tick"`
	DurTick  int64          `json:"dur_tick"`
	Offset   int64          `json:"offset"`
	Length   int64          `json:"length"`
	TimeBase model.Rational `json:"time_base"`
	ID       string         `json:"id"`
	Scheme   string         `json:"scheme,omitempty"`
	Payload  []byte         `json:"payload,omitempty"`
}

// ScanResult is everything one source yielded, plus non-fatal parse anomalies
// for conditions that must survive into the manifest.
type ScanResult struct {
	SourcePath string          `json:"source_path"`
	SourceHash string          `json:"source_hash"`
	Fragmented bool            `json:"fragmented"`
	Tracks     []TrackSummary  `json:"tracks"`
	Samples    []Sample        `json:"samples"`
	Events     []TimedEvent    `json:"events"`
	Anomalies  []model.Anomaly `json:"anomalies"`
}
