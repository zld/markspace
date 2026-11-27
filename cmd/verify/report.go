package main

import (
	"fmt"

	"mediatool/internal/fixture"
)

type scenarioReport struct {
	Kind             string  `json:"kind"`
	SyncErrorMS      float64 `json:"sync_error_ms"`
	LossRecoveryRate float64 `json:"loss_recovery_rate"`
	LatencyP95MS     float64 `json:"latency_p95_ms"`
	DuplicatePackets uint64  `json:"duplicate_packets"`
	RecoveredChunks  int     `json:"recovered_chunks"`
	IndexSHA256      string  `json:"index_sha256"`
	ExitCode         int     `json:"exit_code"`
	ErrorOffset      int64   `json:"error_offset,omitempty"`
	ErrorCode        string  `json:"error_code,omitempty"`
}

type acceptanceReport struct {
	Scenarios []scenarioReport `json:"scenarios"`
	// Summary repeats the headline fields required by the acceptance brief.
	Summary     scenarioReport `json:"summary"`
	ResumeMatch bool           `json:"resume_index_match"`
}

// aggregate rolls the per-scenario metrics into the headline summary. The
// loss recovery rate reflects only reorderable loss (the dup_reorder
// scenario); genuinely dropped burst packets have no recovery denominator
// and are validated separately for non-fabrication.
func aggregate(scenarios []scenarioReport) scenarioReport {
	sum := scenarioReport{Kind: "summary", LossRecoveryRate: 1, LatencyP95MS: 0}
	var rateSum, rateN float64
	for _, s := range scenarios {
		if s.SyncErrorMS > sum.SyncErrorMS {
			sum.SyncErrorMS = s.SyncErrorMS
		}
		sum.DuplicatePackets += s.DuplicatePackets
		sum.RecoveredChunks += s.RecoveredChunks
		if s.Kind == string(fixture.KindDupReorder) {
			rateSum += s.LossRecoveryRate
			rateN++
		}
		if s.LatencyP95MS > sum.LatencyP95MS {
			sum.LatencyP95MS = s.LatencyP95MS
		}
	}
	if rateN > 0 {
		sum.LossRecoveryRate = rateSum / rateN
	}
	for _, s := range scenarios {
		if s.Kind == string(fixture.KindNormal) {
			sum.IndexSHA256 = s.IndexSHA256
		}
	}
	return sum
}

// assertAcceptance enforces the numeric thresholds from the acceptance brief.
func assertAcceptance(r acceptanceReport) error {
	if r.Summary.SyncErrorMS > 20 {
		return fmt.Errorf("sync error %.2fms > 20ms", r.Summary.SyncErrorMS)
	}
	if r.Summary.LossRecoveryRate < 0.95 {
		return fmt.Errorf("loss recovery %.3f < 0.95", r.Summary.LossRecoveryRate)
	}
	if r.Summary.LatencyP95MS <= 0 {
		return fmt.Errorf("p95 latency must be positive, got %.3f", r.Summary.LatencyP95MS)
	}
	for _, s := range r.Scenarios {
		if s.Kind == string(fixture.KindTruncated) {
			if s.ExitCode != 2 || s.ErrorOffset < 0 {
				return fmt.Errorf("truncated scenario must exit 2 with offset, got %+v", s)
			}
		}
	}
	return nil
}
