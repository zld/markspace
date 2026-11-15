package metrics

import (
	"encoding/json"
	"fmt"
	"io"
)

// Report is the terminal output of one relaycheck run. All counters are
// emitted in both JSON and summary form.
type Report struct {
	CandidatePairs     int     `json:"candidate_pairs"`
	RejectedCandidates int     `json:"rejected_candidates"`
	ForwardedPackets   uint64  `json:"forwarded_packets"`
	DroppedPackets     uint64  `json:"dropped_packets"`
	DuplicatePackets   uint64  `json:"duplicate_packets"`
	ReorderedPackets   uint64  `json:"reordered_packets"`
	WriteErrors        uint64  `json:"write_errors"`
	JitterMs           float64 `json:"jitter_ms"`
	JitterSamples      uint64  `json:"jitter_samples"`
	JitterValid        bool    `json:"jitter_valid"`
	DurationMs         int64   `json:"duration_ms"`
}

// WriteJSON renders the report as a single JSON object.
func (r Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteSummary renders a human-readable, grep-friendly summary.
func (r Report) WriteSummary(w io.Writer) error {
	_, err := fmt.Fprintf(w, `candidate_pairs=%d rejected_candidates=%d
forwarded_packets=%d dropped_packets=%d duplicate_packets=%d reordered_packets=%d write_errors=%d
jitter_ms=%.3f jitter_samples=%d jitter_valid=%t
duration_ms=%d
`,
		r.CandidatePairs, r.RejectedCandidates,
		r.ForwardedPackets, r.DroppedPackets, r.DuplicatePackets, r.ReorderedPackets, r.WriteErrors,
		r.JitterMs, r.JitterSamples, r.JitterValid,
		r.DurationMs)
	return err
}
