// Package report turns pipeline outputs into the single deterministic
// acceptance record: threshold verdicts, processing budget, measured
// duration and deviation, anomaly intervals, and one conclusion per item.
package report

import (
	"sort"
	"strconv"
	"time"

	"mediarc/internal/model"
)

// Thresholds carries every configured numeric limit for one run.
type Thresholds struct {
	BoundaryMs       int64
	DriftMsPerSec    int64
	BudgetMs         int64
}

// ItemConclusion is the per-representation verdict. It is stable across
// repeated runs because it is derived solely from inputs and parameters.
type ItemConclusion struct {
	RepID           string   `json:"rep_id"`
	Format          string   `json:"format"`
	Recovered       bool     `json:"recovered"`
	Trusted         bool     `json:"trusted"`
	TrackCount      int      `json:"track_count"`
	WrapCount       int      `json:"wrap_count"`
	DriftPPM        int64    `json:"drift_ppm"`
	DriftExceeded   bool     `json:"drift_exceeded"`
	VFR             bool     `json:"vfr"`
	FingerprintMatch bool    `json:"fingerprint_match"`
	Mapped          bool     `json:"mapped"`
	ReviewRequired  bool     `json:"review_required"`
	ErrorKinds      []string `json:"error_kinds"`
	Conclusion      string   `json:"conclusion"`
}

// Summary is the top-level JSON record printed by the CLI.
type Summary struct {
	Seed                int64             `json:"seed"`
	RepresentationCount int               `json:"representation_count"`
	RecoveredCount      int               `json:"recovered_count"`
	AnomalyCount        int               `json:"anomaly_count"`
	MappingSuccessCount int               `json:"mapping_success_count"`
	FingerprintMatches  int               `json:"fingerprint_match_count"`
	BudgetMs            int64             `json:"budget_ms"`
	ElapsedMs           int64             `json:"elapsed_ms"`
	BudgetDeviationMs   int64             `json:"budget_deviation_ms"`
	BudgetExceeded      bool              `json:"budget_exceeded"`
	BoundaryThresholdMs int64             `json:"boundary_threshold_ms"`
	Conclusions         []ItemConclusion  `json:"conclusions"`
	Anomalies           []AnomalyView     `json:"anomalies"`
	ReviewItems         []string          `json:"review_items"`
}

// AnomalyView is the JSON projection of model.AnomalyInterval with the
// participating representations.
type AnomalyView struct {
	StartMs      int64    `json:"start_ms"`
	EndMs        int64    `json:"end_ms"`
	ThresholdMs  int64    `json:"threshold_ms"`
	MaxDeltaMs   int64    `json:"max_delta_ms"`
	Participants []string `json:"participants"`
	Kind         string   `json:"kind"`
	Detail       string   `json:"detail"`
}

// ItemInput bundles everything one representation produced.
type ItemInput struct {
	Rep              model.RecoveredRepresentation
	FingerprintMatch bool
	// MappingParticipant marks representations whose tracks were submitted to
	// the explicit mapping plan. Non-participants cannot be mapping failures.
	MappingParticipant bool
	Mapped             bool
	ReviewRequired     bool
}

// Input is the full report assembly request. Elapsed is measured by the
// caller around the pipeline; the report never times work itself.
type Input struct {
	Seed       int64
	Thresholds Thresholds
	Elapsed    time.Duration
	Items      []ItemInput
	FanIn      modelFanIn
	ReviewIDs  []string
}

// modelFanIn is the slice of fan-in output the report needs.
type modelFanIn struct {
	Anomalies []model.AnomalyInterval
	Untrusted []string
}

// NewFanInView adapts a clock.FanInResult without importing the clock package
// (avoids an import cycle in future callers).
func NewFanInView(anomalies []model.AnomalyInterval, untrusted []string) modelFanIn {
	return modelFanIn{Anomalies: anomalies, Untrusted: untrusted}
}

// Build assembles the deterministic summary. Sorting every slice by a stable
// key makes repeated runs with identical inputs produce identical JSON.
func Build(in Input) Summary {
	sum := Summary{
		Seed:                in.Seed,
		RepresentationCount: len(in.Items),
		BudgetMs:            in.Thresholds.BudgetMs,
		ElapsedMs:           in.Elapsed.Milliseconds(),
		BoundaryThresholdMs: in.Thresholds.BoundaryMs,
	}
	sum.BudgetDeviationMs = sum.ElapsedMs - sum.BudgetMs
	sum.BudgetExceeded = sum.BudgetMs > 0 && sum.ElapsedMs > sum.BudgetMs

	sum.Anomalies = make([]AnomalyView, 0, len(in.FanIn.Anomalies))
	for _, a := range in.FanIn.Anomalies {
		participants := append([]string(nil), a.Participants...)
		sort.Strings(participants)
		sum.Anomalies = append(sum.Anomalies, AnomalyView{
			StartMs: a.StartMs, EndMs: a.EndMs, ThresholdMs: a.ThresholdMs,
			MaxDeltaMs: a.MaxDeltaMs, Participants: participants,
			Kind: string(a.Kind), Detail: a.Detail,
		})
	}
	sort.Slice(sum.Anomalies, func(i, j int) bool {
		if sum.Anomalies[i].StartMs != sum.Anomalies[j].StartMs {
			return sum.Anomalies[i].StartMs < sum.Anomalies[j].StartMs
		}
		return sum.Anomalies[i].Detail < sum.Anomalies[j].Detail
	})
	sum.AnomalyCount = len(sum.Anomalies)

	seenReview := map[string]bool{}
	for _, id := range in.FanIn.Untrusted {
		if !seenReview[id] {
			sum.ReviewItems = append(sum.ReviewItems, id)
			seenReview[id] = true
		}
	}
	for _, id := range in.ReviewIDs {
		if !seenReview[id] {
			sum.ReviewItems = append(sum.ReviewItems, id)
			seenReview[id] = true
		}
	}
	sort.Strings(sum.ReviewItems)

	for _, item := range in.Items {
		sum.Conclusions = append(sum.Conclusions, conclude(item, in.Thresholds))
	}
	sort.Slice(sum.Conclusions, func(i, j int) bool {
		return sum.Conclusions[i].RepID < sum.Conclusions[j].RepID
	})

	for _, c := range sum.Conclusions {
		if c.Recovered {
			sum.RecoveredCount++
		}
		if c.FingerprintMatch {
			sum.FingerprintMatches++
		}
		if c.Mapped {
			sum.MappingSuccessCount++
		}
	}
	return sum
}

func conclude(item ItemInput, th Thresholds) ItemConclusion {
	rep := item.Rep
	c := ItemConclusion{
		RepID: rep.ID, Format: rep.Format,
		Recovered: rep.OK, Mapped: item.Mapped,
		FingerprintMatch: item.FingerprintMatch,
		ReviewRequired:   item.ReviewRequired,
	}
	mappingParticipant := item.MappingParticipant
	kinds := map[string]bool{}
	trusted := true
	for _, rt := range rep.Tracks {
		c.TrackCount++
		c.WrapCount += rt.WrapCount
		if rt.DriftPPM != 0 {
			// Keep the largest-magnitude drift for the record.
			if abs64(rt.DriftPPM) > abs64(c.DriftPPM) {
				c.DriftPPM = rt.DriftPPM
			}
		}
		if rt.VFR {
			c.VFR = true
		}
		if !rt.Trusted {
			trusted = false
		}
	}
	c.Trusted = trusted
	c.DriftExceeded = th.DriftMsPerSec > 0 && abs64(c.DriftPPM) > th.DriftMsPerSec*1000
	for _, e := range rep.Errors {
		kinds[string(e.Kind)] = true
	}
	for k := range kinds {
		c.ErrorKinds = append(c.ErrorKinds, k)
	}
	sort.Strings(c.ErrorKinds)
	c.Conclusion = classify(c, kinds, mappingParticipant)
	return c
}

func classify(c ItemConclusion, kinds map[string]bool, mappingParticipant bool) string {
	if c.ReviewRequired || kinds[string(model.KindUntrustedBoundary)] {
		return "review_required"
	}
	if !c.Recovered {
		if kinds[string(model.KindSegmentCorrupt)] {
			return "corrupt_isolated"
		}
		return "recovery_failed"
	}
	if c.DriftExceeded || kinds[string(model.KindDriftExceeded)] {
		return "drift_threshold_exceeded"
	}
	if !c.FingerprintMatch {
		return "remux_mismatch"
	}
	if mappingParticipant && !c.Mapped {
		return "mapping_rejected"
	}
	return "ok"
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// BudgetNote renders a human-readable budget line for terminal output.
func (s Summary) BudgetNote() string {
	state := "within budget"
	if s.BudgetExceeded {
		state = "OVER budget"
	}
	return "elapsed " + strconv.FormatInt(s.ElapsedMs, 10) + "ms vs budget " +
		strconv.FormatInt(s.BudgetMs, 10) + "ms (deviation " +
		strconv.FormatInt(s.BudgetDeviationMs, 10) + "ms, " + state + ")"
}
