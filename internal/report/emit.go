// Package report renders pipeline results to the terminal and to JSON
// files. It only ever receives identifiers and statuses: raw key material
// and license tokens are not part of model.Report and can never be emitted.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"mediakey/internal/model"
)

// PrintSummary writes the aggregate counters expected by the demo contract.
func PrintSummary(w io.Writer, rep *model.Report) {
	fmt.Fprintf(w, "rotation_count=%d\n", rep.RotationCount)
	fmt.Fprintf(w, "shard_count=%d\n", rep.ShardCount)
	fmt.Fprintf(w, "accepted=%d\n", rep.Accepted)
	fmt.Fprintf(w, "rejected=%d\n", rep.Rejected)
}

// PrintShards writes one stable, shard-ordered line per shard.
func PrintShards(w io.Writer, rep *model.Report) {
	results := append([]model.ShardResult(nil), rep.Results...)
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].ShardID < results[j].ShardID
	})
	for _, r := range results {
		fmt.Fprintf(w, "shard_id=%d key_id=%s status=%s failure_reason=%s\n",
			r.ShardID, r.KeyID, r.Status, r.FailureReason)
	}
}

// Print writes both the summary block and the per-shard lines.
func Print(w io.Writer, rep *model.Report) {
	PrintSummary(w, rep)
	PrintShards(w, rep)
}

// WriteJSON serializes the full report to path with indented JSON. Parent
// directories must already exist.
func WriteJSON(path string, rep *model.Report) error {
	ordered := append([]model.ShardResult(nil), rep.Results...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].ShardID < ordered[j].ShardID
	})
	cp := *rep
	cp.Results = ordered
	data, err := json.MarshalIndent(&cp, "", "  ")
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write report %s: %w", path, err)
	}
	return nil
}
