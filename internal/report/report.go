// Package report assembles the structured, deterministic run summary printed
// by the ingest CLI and compared across repeat runs.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Field is one stable key/value line in the acceptance output.
type Field struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Report carries every acceptance-visible measurement. JSON field order is
// fixed via the explicit Fields rendering, so two identical runs print
// byte-identical summaries.
type Report struct {
	CaseCount         int    `json:"case_count"`
	FaultCount        int    `json:"fault_count"`
	EarliestBadPacket uint64 `json:"earliest_bad_packet"`
	LocateErrorNS     int64  `json:"locate_error_ns"`
	RecoveryMS        int64  `json:"recovery_ms"`
	RollbackStage     string `json:"rollback_stage"`
	CommitBoundary    string `json:"commit_boundary"`
	SHA256            string `json:"sha256"`
	RepeatSHAEqual    bool   `json:"repeat_sha256_equal"`
}

// Fields renders the exact terminal field set required by the acceptance
// contract in a fixed, greppable key=value order.
func (r Report) Fields() []Field {
	boundary := r.CommitBoundary
	if boundary == "" {
		boundary = "<none>"
	}
	return []Field{
		{"case_count", fmt.Sprintf("%d", r.CaseCount)},
		{"fault_count", fmt.Sprintf("%d", r.FaultCount)},
		{"earliest_bad_packet", fmt.Sprintf("%d", r.EarliestBadPacket)},
		{"locate_error_ns", fmt.Sprintf("%d", r.LocateErrorNS)},
		{"recovery_ms", fmt.Sprintf("%d", r.RecoveryMS)},
		{"rollback_stage", r.RollbackStage},
		{"commit_boundary", boundary},
		{"sha256", r.SHA256},
		{"repeat_sha256_equal", fmt.Sprintf("%t", r.RepeatSHAEqual)},
	}
}

// Text renders one field per line as key=value.
func (r Report) Text() string {
	var b strings.Builder
	for _, f := range r.Fields() {
		b.WriteString(f.Key)
		b.WriteByte('=')
		b.WriteString(f.Value)
		b.WriteByte('\n')
	}
	return b.String()
}

// JSON renders the same data as a single ordered JSON object.
func (r Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// CanonicalHash folds a sorted set of named byte chunks into one SHA-256.
// Sorting by name makes the hash independent of map iteration order.
func CanonicalHash(parts map[string][]byte) string {
	names := make([]string, 0, len(parts))
	for n := range parts {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write(parts[n])
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// CheckHexSHA256 validates a 64-character lowercase hexadecimal digest.
func CheckHexSHA256(s string) error {
	if len(s) != 64 {
		return fmt.Errorf("digest %q has length %d, want 64", s, len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return fmt.Errorf("digest %q is not hexadecimal: %w", s, err)
	}
	return nil
}
