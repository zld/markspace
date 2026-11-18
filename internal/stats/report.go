package stats

import (
	"encoding/json"
	"io"
	"time"

	"relaylab/internal/forward"
)

// CandidatePair summarises one validated ICE candidate pair. Addresses are
// loopback-only by construction; no external host information is emitted.
type CandidatePair struct {
	LocalAddr  string `json:"local_addr"`
	RemoteAddr string `json:"remote_addr"`
	Protocol   string `json:"protocol"`
	Type       string `json:"type"`
}

// Report is the terminal JSON summary of one experiment run.
type Report struct {
	Status            string                `json:"status"`
	Seed              int64                 `json:"seed"`
	DurationMS        int64                 `json:"duration_ms"`
	CandidatePairs    []CandidatePair       `json:"candidate_pairs"`
	SentPackets       int64                 `json:"sent_packets"`
	ForwardedPackets  int64                 `json:"forwarded_packets"`
	DroppedPackets    int64                 `json:"dropped_packets"`
	DuplicatePackets  int64                 `json:"duplicate_packets"`
	ReorderedPackets  int64                 `json:"reordered_packets"`
	MalformedPackets  int64                 `json:"malformed_packets"`
	UnforwardedAtExit int64                 `json:"unforwarded_at_exit"`
	JitterRTPTicks    uint64                `json:"jitter_rtp_ticks"`
	JitterMS          float64               `json:"jitter_ms"`
	ShutdownClean     bool                  `json:"shutdown_clean"`
	Signaling         SignalingCounters     `json:"signaling"`
	Relay             forward.RelayStats    `json:"relay"`
	Elapsed           time.Duration         `json:"-"`
}

// SignalingCounters mirrors the server-side rejection counters.
type SignalingCounters struct {
	DuplicateMessages int `json:"duplicate_messages"`
	StaleMessages     int `json:"stale_messages"`
	InvalidMessages   int `json:"invalid_messages"`
}

// NewReport allocates a report with empty (not nil) slice fields so the JSON
// always renders candidate_pairs as [].
func NewReport(seed int64) *Report {
	return &Report{CandidatePairs: []CandidatePair{}, Status: "running", Seed: seed}
}

// FillJitter copies the estimator's final values into the report.
func (r *Report) FillJitter(j *JitterTracker) {
	r.JitterRTPTicks = j.JitterTicks()
	r.JitterMS = j.JitterMS()
}

// FillRelay maps relay counters onto the top-level report fields.
func (r *Report) FillRelay(s forward.RelayStats, pending int64) {
	r.Relay = s
	r.ForwardedPackets = s.Forwarded
	r.DroppedPackets = s.Dropped
	r.DuplicatePackets = s.Duplicates
	r.ReorderedPackets = s.Reordered
	r.MalformedPackets = s.Malformed
	r.UnforwardedAtExit = pending
}

// Finalize stamps duration, status and the clean-shutdown flag.
func (r *Report) Finalize(status string, clean bool, elapsed time.Duration) {
	r.Status = status
	r.ShutdownClean = clean
	r.Elapsed = elapsed
	r.DurationMS = elapsed.Milliseconds()
}

// WriteJSON emits the report as indented JSON followed by a newline.
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return err
	}
	return nil
}
