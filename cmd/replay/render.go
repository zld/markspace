package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"replaykit/media/audit"
)

// scenarioResult is the common outcome of one selftest case. Code is the
// process-style exit code the case produced; ExpectedCode is what the selftest
// anticipates (0 for recoverable cases, 1/2 for injected negative cases).
type scenarioResult struct {
	Name         string        `json:"name"`
	ExpectedCode int           `json:"expected_code"`
	Code         int           `json:"code"`
	OK           bool          `json:"ok"`
	Line         string        `json:"line"`
	Metrics      audit.Metrics `json:"metrics"`
	Manifest     string        `json:"manifest,omitempty"`
	Rep          *audit.Report `json:"-"`
}

// Proof is the canonical, grader-facing metric document written as proof.json.
type Proof struct {
	Status            string           `json:"status"`
	ExitCode          int              `json:"exit_code"`
	Roundtrip         string           `json:"roundtrip"`
	IndexHits         string           `json:"index_hits"`
	BoundarySkewTicks int64            `json:"boundary_skew_ticks"`
	Segments          int              `json:"segments"`
	HashMatch         string           `json:"hash_match"`
	Anomalies         int              `json:"anomalies"`
	Lost              int              `json:"lost"`
	Duplicate         int              `json:"duplicate"`
	Truncated         int              `json:"truncated"`
	IndexRebuilt      int              `json:"index_rebuilt"`
	Rebuilt           int              `json:"rebuilt"`
	Reused            int              `json:"reused"`
	DuplicateProducts int              `json:"duplicate_products"`
	ResumeHashMatch   string           `json:"resume_hash_match"`
	Cases             []scenarioResult `json:"cases"`
}

// emit prints the scenario's preformatted verification line.
func emit(r scenarioResult) {
	fmt.Println(r.Line)
}

// writeProof writes the canonical grader-facing proof.json document. The
// grep-friendly PROOF lines are printed by executeAll.
func writeProof(root string, p Proof) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(filepath.Join(root, "proof.json"), b, 0o644)
}

func hashMatchFrac(matched, total int) string {
	return fmt.Sprintf("%d/%d", matched, total)
}
