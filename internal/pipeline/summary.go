// Package pipeline wires every subsystem together: fixture input flows
// through clock repair, RTP recovery and index rebuild, all driven by the
// DAG scheduler with journaled checkpoints, and ends in a deterministic
// summary suitable for terminal output and acceptance checks.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"mediasync/internal/clock"
)

// Summary is the canonical, JSON-serializable run result. Field names match
// the acceptance contract exactly.
type Summary struct {
	Seed                int64   `json:"seed"`
	DurationMs          int64   `json:"duration_ms"`
	SyncErrorMs         float64 `json:"sync_error_ms"`
	PacketRecoveryRate  float64 `json:"packet_recovery_rate"`
	P95LatencyMs        float64 `json:"p95_latency_ms"`
	IndexEntries        int     `json:"index_entries"`
	ValidIntervals      int     `json:"valid_intervals"`
	DroppedIndexRecords int     `json:"dropped_index_records"`
	TruncatedBytes      int64   `json:"truncated_bytes"`
	BlockedDependency   string  `json:"blocked_dependency"`
	OutputSHA256        string  `json:"output_sha256"`
	ExitCode            int     `json:"exit_code"`
}

// StageCounters collects per-stage observability used to build the summary.
type StageCounters struct {
	PacketsExpected int64
	PacketsReceived int64
	Duplicates      int64
	Lost            int64
	Concealed       int64
	Blocked         int64
	Reasons         map[clock.RepairReason]int
}

// Digest computes a stable SHA-256 over the canonical JSON of v. The same
// logical value always maps to the same digest regardless of map ordering,
// because encoding/json sorts map keys.
func Digest(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", clock.AsMediaError(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// MustDigest panics on error; used only where the value is known-marshalable.
func MustDigest(v any) string {
	d, err := Digest(v)
	if err != nil {
		panic(err)
	}
	return d
}

// stableParams returns a deterministic JSON encoding of a parameter map.
func stableParams(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buf := make([]byte, 0, 64)
	for _, k := range keys {
		buf = append(buf, k...)
		buf = append(buf, '=')
		buf = append(buf, m[k]...)
		buf = append(buf, ';')
	}
	return string(buf)
}
