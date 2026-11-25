// Package report renders deterministic mappings and stable result summaries.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"archivecheck/internal/pipeline"
	"archivecheck/internal/policy"
)

const SchemaVersion = "archivecheck-summary-v1"

// Mapping is one input-to-output audit row.
type Mapping struct {
	Index          int             `json:"index"`
	Input          string          `json:"input"`
	Output         string          `json:"output"`
	Sample         int             `json:"sample"`
	Status         pipeline.Status `json:"status"`
	TimeDeltaNS    int64           `json:"time_delta_ns"`
	ByteDelta      int64           `json:"byte_delta"`
	InputBytes     int             `json:"input_bytes"`
	OutputBytes    int             `json:"output_bytes"`
	EstimatedBytes int64           `json:"estimated_bytes"`
	FailurePhase   string          `json:"failure_phase,omitempty"`
	FailureCode    string          `json:"failure_code,omitempty"`
	FailureSample  int             `json:"failure_sample,omitempty"`
	FailureOffset  int64           `json:"failure_offset,omitempty"`
	FailureMessage string          `json:"failure_message,omitempty"`
	Config         *policy.Config  `json:"config,omitempty"`
}

// Summary is the stable, machine-readable audit result.
type Summary struct {
	SchemaVersion    string          `json:"schema_version"`
	Seed             int64           `json:"seed"`
	BudgetBytes      int64           `json:"budget_bytes,omitempty"`
	TotalOutputBytes int             `json:"total_output_bytes"`
	BudgetWithin     bool            `json:"budget_within"`
	Counts           pipeline.Counts `json:"counts"`
	Mappings         []Mapping       `json:"mappings"`
	StableDigest     string          `json:"stable_digest"`
}

// Build creates a sorted summary and computes its stable digest. It deliberately
// does not include wall duration, worker count, or completion order.
func Build(seed int64, budgetBytes int64, results []pipeline.Result) *Summary {
	sorted := pipeline.MergeOrdered(results)
	s := &Summary{
		SchemaVersion: SchemaVersion,
		Seed:          seed,
		BudgetBytes:   budgetBytes,
		Counts:        pipeline.Counts{Total: len(sorted)},
		Mappings:      make([]Mapping, 0, len(sorted)),
	}

	for _, r := range sorted {
		m := Mapping{
			Index:          r.Index,
			Input:          r.Input,
			Output:         r.Output,
			Sample:         r.Sample,
			Status:         r.Status,
			TimeDeltaNS:    r.TimeDeltaNS,
			ByteDelta:      r.ByteDelta,
			InputBytes:     r.InputBytes,
			OutputBytes:    r.OutputByteCount,
			EstimatedBytes: r.EstimatedBytes,
		}
		if cfg, ok := r.Details.(policy.Config); ok {
			cfgCopy := cfg
			m.Config = &cfgCopy
		} else if cfg, ok := r.Details.(*policy.Config); ok {
			m.Config = cfg
		}
		if r.Diagnostic != nil {
			m.FailurePhase = string(r.Diagnostic.Phase)
			m.FailureCode = string(r.Diagnostic.Code)
			m.FailureSample = r.Diagnostic.Sample
			m.FailureOffset = r.Diagnostic.Offset
			m.FailureMessage = r.Diagnostic.Message
		}
		s.Mappings = append(s.Mappings, m)

		switch r.Status {
		case pipeline.StatusSuccess:
			s.Counts.Success++
			s.Counts.Completed++
			s.TotalOutputBytes += r.OutputByteCount
		case pipeline.StatusFailed:
			s.Counts.Failed++
			s.Counts.Completed++
		case pipeline.StatusCanceled:
			s.Counts.Canceled++
		case pipeline.StatusNotStarted:
			s.Counts.NotStarted++
		}
	}
	if budgetBytes > 0 {
		s.BudgetWithin = int64(s.TotalOutputBytes) <= budgetBytes
	} else {
		s.BudgetWithin = true
	}
	s.StableDigest = computeDigest(s)
	return s
}

type digestDocument struct {
	SchemaVersion    string          `json:"schema_version"`
	Seed             int64           `json:"seed"`
	BudgetBytes      int64           `json:"budget_bytes,omitempty"`
	TotalOutputBytes int             `json:"total_output_bytes"`
	BudgetWithin     bool            `json:"budget_within"`
	Counts           pipeline.Counts `json:"counts"`
	Mappings         []Mapping       `json:"mappings"`
}

func computeDigest(s *Summary) string {
	doc := digestDocument{
		SchemaVersion:    s.SchemaVersion,
		Seed:             s.Seed,
		BudgetBytes:      s.BudgetBytes,
		TotalOutputBytes: s.TotalOutputBytes,
		BudgetWithin:     s.BudgetWithin,
		Counts:           s.Counts,
		Mappings:         s.Mappings,
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return "digest-error"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// CanonicalJSON returns indented JSON with mappings in stable order.
func (s *Summary) CanonicalJSON() ([]byte, error) {
	copySummary := *s
	copySummary.StableDigest = ""
	b, err := json.MarshalIndent(&copySummary, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// FormatTerminal renders the required key=value mapping lines and quantized summary.
func (s *Summary) FormatTerminal() string {
	var b strings.Builder
	for _, m := range s.Mappings {
		fmt.Fprintf(&b, "item index=%d sample=%d input=%s output=%s status=%s time_delta_ns=%d byte_delta=%d input_bytes=%d output_bytes=%d estimated_bytes=%d\n",
			m.Index, m.Sample, m.Input, dash(m.Output), m.Status, m.TimeDeltaNS, m.ByteDelta,
			m.InputBytes, m.OutputBytes, m.EstimatedBytes)
		if m.Config != nil {
			fmt.Fprintf(&b, "  config feasible=%t profile=%s quality_target=%d max_bitrate_bps=%d budget_bytes=%d estimated_bytes=%d\n",
				m.Config.Feasible, m.Config.Selected.Profile, m.Config.QualityTarget,
				m.Config.MaxBitrateBps, m.Config.BudgetBytes, m.Config.EstimatedBytes)
			for _, reason := range m.Config.Reasons {
				fmt.Fprintf(&b, "  reason: %s\n", reason)
			}
		}
		if m.FailureCode != "" {
			fmt.Fprintf(&b, "  failure phase=%s code=%s sample=%d offset=%d message=%q\n",
				m.FailurePhase, m.FailureCode, m.FailureSample, m.FailureOffset, m.FailureMessage)
		}
	}
	fmt.Fprintf(&b, "counts total=%d success=%d failed=%d completed=%d canceled=%d not_started=%d\n",
		s.Counts.Total, s.Counts.Success, s.Counts.Failed, s.Counts.Completed, s.Counts.Canceled, s.Counts.NotStarted)
	fmt.Fprintf(&b, "budget budget_bytes=%d total_output_bytes=%d within=%t\n",
		s.BudgetBytes, s.TotalOutputBytes, s.BudgetWithin)
	fmt.Fprintf(&b, "stable_digest=%s\n", s.StableDigest)
	return b.String()
}

// SortedFailureLocations returns stable "sample:offset:code" strings for tests.
func (s *Summary) SortedFailureLocations() []string {
	var out []string
	for _, m := range s.Mappings {
		if m.FailureCode != "" {
			out = append(out, fmt.Sprintf("%d:%d:%s:%s", m.FailureSample, m.FailureOffset, m.FailurePhase, m.FailureCode))
		}
	}
	sort.Strings(out)
	return out
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
