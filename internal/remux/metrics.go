package remux

import (
	"sort"
	"time"

	"mediaqc/internal/timeline"
)

// RepairCounts mirrors timeline.RepairStats for the JSON summary.
type RepairCounts struct {
	Rebased     int `json:"rebased"`
	Wraps       int `json:"wraps"`
	Regressions int `json:"regressions"`
}

// Summary is the per-run JSON report printed to the terminal. The
// quantified fields are all derived from the generated samples; no
// external media is involved.
type Summary struct {
	Input                  string       `json:"input"`
	Output                 string       `json:"output,omitempty"`
	InputSHA256            string       `json:"input_sha256"`
	OutputSHA256           string       `json:"output_sha256,omitempty"`
	IndexEntries           int          `json:"index_entries"`
	IndexChecksum          string       `json:"index_checksum"`
	IndexAction            string       `json:"index_action"`
	ResumeAction           string       `json:"resume_action"`
	Repairs                RepairCounts `json:"repairs"`
	SyncErrorMs            float64      `json:"sync_error_ms"`
	PacketLossRecoveryRate float64      `json:"packet_loss_recovery_rate"`
	P95LatencyMs           float64      `json:"p95_latency_ms"`
	PeakBufferBytes        int64        `json:"peak_buffer_bytes"`
	Packets                int          `json:"packets"`
	ErrorClass            string       `json:"error_class,omitempty"`
	Offset                 int64        `json:"offset,omitempty"`
	Track                  uint64       `json:"track,omitempty"`
	Message                string       `json:"message,omitempty"`
}

// FromFault fills the error fields of the summary from a fault.
func (s *Summary) FromFault(f *timeline.Fault) {
	if f == nil {
		return
	}
	s.ErrorClass = string(f.Class)
	s.Offset = f.Offset
	s.Track = f.Track
	s.Message = f.Msg
}

// P95 returns the 95th percentile of the given durations (nearest
// rank), or 0 for an empty slice.
func P95(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(ds))
	copy(sorted, ds)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := (95*len(sorted) + 99) / 100 // ceil(0.95*n)
	return sorted[rank-1]
}
