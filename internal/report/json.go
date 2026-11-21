// Package report renders the stable machine-readable run summary.
package report

import (
	"encoding/json"
	"io"

	"looprelay/internal/metrics"
)

// Status values.
const (
	StatusOK     = "ok"
	StatusFailed = "failed"
)

// Link is the selected candidate pair of one forwarding hop.
type Link struct {
	Name          string `json:"name"`
	Connected     bool   `json:"connected"`
	LocalAddress  string `json:"local_address"`
	LocalPort     uint16 `json:"local_port"`
	RemoteAddress string `json:"remote_address"`
	RemotePort    uint16 `json:"remote_port"`
	Note          string `json:"note,omitempty"`
}

// Check is one verifiable acceptance condition.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// SequenceSummary mirrors the sequence analysis with stable field names.
type SequenceSummary struct {
	Received      int    `json:"received"`
	ExpectedSpan  int    `json:"expected_span"`
	Lost          int    `json:"lost"`
	Duplicates    int    `json:"duplicates"`
	Reordered     int    `json:"reordered"`
	FirstSequence uint16 `json:"first_sequence"`
	LastSequence  uint16 `json:"last_sequence"`
}

// TimestampSummary mirrors timestamp continuity with stable field names.
type TimestampSummary struct {
	Received       int    `json:"received"`
	Wraps          int    `json:"wraps"`
	Regressions    int    `json:"regressions"`
	FirstTimestamp uint32 `json:"first_timestamp"`
	LastTimestamp  uint32 `json:"last_timestamp"`
	TotalSamples   int64  `json:"total_samples"`
}

// Report is the complete run summary. Field order is part of the output
// contract; do not reorder without updating the fixtures.
type Report struct {
	Status             string                `json:"status"`
	Command            string                `json:"command"`
	RequestID          int64                 `json:"request_id"`
	Listen             string                `json:"listen"`
	CandidatePairCount int                   `json:"candidate_pair_count"`
	Links              []Link                `json:"links"`
	GeneratedPackets   int                   `json:"generated_packets"`
	SkippedPackets     int                   `json:"skipped_packets"`
	SentPackets        int                   `json:"sent_packets"`
	ForwardedPackets   uint64                `json:"forwarded_packets"`
	ReceivedPackets    int                   `json:"received_packets"`
	LostPackets        int                   `json:"lost_packets"`
	DuplicatePackets   int                   `json:"duplicate_packets"`
	ReorderedPackets   int                   `json:"reordered_packets"`
	QueueDrops         uint64                `json:"queue_drops"`
	WriteFailures      uint64                `json:"write_failures"`
	JitterMS           metrics.JitterSummary `json:"jitter_ms"`
	Sequence           SequenceSummary       `json:"sequence"`
	Timestamps         TimestampSummary      `json:"timestamps"`
	Checks             []Check               `json:"checks"`
	DurationMS         int64                 `json:"duration_ms"`
	Error              string                `json:"error,omitempty"`
}

// ProjectSequence converts analyzer output into the report shape.
func ProjectSequence(s metrics.SequenceReport) SequenceSummary {
	return SequenceSummary{
		Received:      s.Received,
		ExpectedSpan:  s.ExpectedSpan,
		Lost:          s.Lost,
		Duplicates:    s.Duplicates,
		Reordered:     s.Reordered,
		FirstSequence: s.FirstSeq,
		LastSequence:  s.LastSeq,
	}
}

// ProjectTimestamps converts timestamp analyzer output.
func ProjectTimestamps(t metrics.TimestampReport) TimestampSummary {
	return TimestampSummary{
		Received:       t.Received,
		Wraps:          t.WrapCount,
		Regressions:    t.Regressions,
		FirstTimestamp: t.FirstTimestamp,
		LastTimestamp:  t.LastTimestamp,
		TotalSamples:   t.TotalSamples,
	}
}

// Passed reports whether every check and the status indicate success.
func (r Report) Passed() bool {
	if r.Status != StatusOK {
		return false
	}
	for _, c := range r.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

// WriteJSON emits deterministic, indented JSON with one trailing newline.
func WriteJSON(w io.Writer, r Report) error {
	if r.Links == nil {
		r.Links = []Link{}
	}
	if r.Checks == nil {
		r.Checks = []Check{}
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
