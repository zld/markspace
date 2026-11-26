package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"replaykit/media/fault"
)

// SegmentState is the recovery state of one output artifact.
type SegmentState string

const (
	StatePlanned SegmentState = "planned"
	StateBuilt   SegmentState = "built"
	StateReused  SegmentState = "reused"
	StateRebuilt SegmentState = "rebuilt"
	StatePartial SegmentState = "partial"
	StateFailed  SegmentState = "failed"
)

// SegmentRecord is one row of the delivery manifest: identity, exact range,
// boundary check, output hash and whether it was reused or rebuilt.
type SegmentRecord struct {
	Key           string       `json:"key"` // deterministic event/layer identity
	EventID       string       `json:"event_id"`
	Layer         string       `json:"layer"`
	Path          string       `json:"path"`
	InputDigest   string       `json:"input_digest"`
	RangeStartNS  int64        `json:"range_start_ns"`
	RangeEndNS    int64        `json:"range_end_ns"`
	MediaStart    int64        `json:"media_start_ticks"`
	MediaEnd      int64        `json:"media_end_ticks"`
	Keyframe      int64        `json:"keyframe_ticks"`
	SampleCount   int          `json:"sample_count"`
	Bytes         int          `json:"bytes"`
	Hash          string       `json:"sha256"`
	ExpectedHash  string       `json:"expected_sha256,omitempty"`
	HashMatch     bool         `json:"hash_match"`
	BoundarySkew  int64        `json:"boundary_skew_ticks"`
	Partial       bool         `json:"partial"`
	State         SegmentState `json:"state"`
	UncoveredFrom int64        `json:"uncovered_from_ticks,omitempty"`
	UncoveredTo   int64        `json:"uncovered_to_ticks,omitempty"`
}

// Boundary records one cross-layer shared-clock boundary核对.
type Boundary struct {
	EventID     string `json:"event_id"`
	Layer       string `json:"layer"`
	Edge        string `json:"edge"` // "start" or "end"
	TargetNS    int64  `json:"target_ns"`
	ChosenNS    int64  `json:"chosen_ns"`
	SkewTicks   int64  `json:"skew_ticks"`
	FrameTicks  int64  `json:"frame_ticks"`
	WithinFrame bool   `json:"within_one_frame"`
}

// Metrics is the flat counter set printed for verification.
type Metrics struct {
	RoundtripTotal    int   `json:"roundtrip_total"`
	RoundtripOK       int   `json:"roundtrip_ok"`
	RoundtripPct      int   `json:"roundtrip_pct"`
	IndexLookups      int   `json:"index_lookups"`
	IndexHits         int   `json:"index_hits"`
	IndexHitsPct      int   `json:"index_hits_pct"`
	BoundarySkewTicks int64 `json:"boundary_skew_ticks"`
	Segments          int   `json:"segments"`
	HashMatched       int   `json:"hash_matched"`
	HashTotal         int   `json:"hash_total"`
	Anomalies         int   `json:"anomalies"`
	Lost              int   `json:"lost"`
	Duplicate         int   `json:"duplicate"`
	Late              int   `json:"late"`
	Truncated         int   `json:"truncated"`
	IndexRebuilt      int   `json:"index_rebuilt"`
	Rebuilt           int   `json:"rebuilt"`
	Reused            int   `json:"reused"`
	DuplicateProducts int   `json:"duplicate_products"`
	Partial           int   `json:"partial"`
	Unrecoverable     int   `json:"unrecoverable"`
}

// Report is the deterministic audit document for one case or whole run.
type Report struct {
	Case       string          `json:"case"`
	Status     string          `json:"status"` // pass | expected_exit | fail
	ExitCode   int             `json:"exit_code"`
	OutDir     string          `json:"out_dir,omitempty"`
	Metrics    Metrics         `json:"metrics"`
	Segments   []SegmentRecord `json:"segments"`
	Boundaries []Boundary      `json:"boundaries"`
	Anomalies  []fault.Anomaly `json:"anomalies"`
	Notes      []string        `json:"notes,omitempty"`
	ledger     fault.Ledger
}

// NewReport builds an empty report for case.
func NewReport(caseName string) *Report {
	return &Report{Case: caseName, Status: "pass"}
}

// Add records one anomaly through the shared ledger.
func (r *Report) Add(a fault.Anomaly) { r.ledger.Add(a) }

// AddAll records many anomalies.
func (r *Report) AddAll(as []fault.Anomaly) {
	for _, a := range as {
		r.ledger.Add(a)
	}
}

// Record maps a recoverable error onto the manifest.
func (r *Report) Record(err error) { r.ledger.Record(err) }

// Note appends a deterministic human-readable note.
func (r *Report) Note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}

// Finalize sorts and sequence-numbers anomalies and syncs Metrics.Anomalies.
// It must be called before serialising or printing counters.
func (r *Report) Finalize() {
	r.Anomalies = r.ledger.Anomalies()
	r.Metrics.Anomalies = len(r.Anomalies)
}

// WriteJSON writes the report as indented deterministic JSON, creating dir.
func (r *Report) WriteJSON(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}

// WriteManifest is an alias that writes manifest.json into dir.
func (r *Report) WriteManifest(dir string) (string, error) {
	p := filepath.Join(dir, "manifest.json")
	return p, r.WriteJSON(p)
}
