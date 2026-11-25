// Package report turns analysis findings into a deterministic, re-runnable
// investigation report: budget evaluation, located anomaly intervals with
// threshold verdicts and per-item processing conclusions.
package report

import (
	"encoding/json"

	"tracecheck/internal/merge"
	"tracecheck/internal/model"
)

// Budget is the configurable business threshold set. Exceeding either
// limit fails the report even though the analysis itself completed.
type Budget struct {
	MaxLossPackets      int        `json:"max_loss_packets"`
	MaxKeyframeInterval model.Tick `json:"max_keyframe_interval_ticks"`
}

// Summary is the stable headline block printed and serialized every run.
type Summary struct {
	PacketCount      int  `json:"packet_count"`
	TrackCount       int  `json:"track_count"`
	KeyframeCount    int  `json:"keyframe_count"`
	LossCount        int  `json:"loss_count"`
	DuplicateCount   int  `json:"duplicate_count"`
	TruncatedCount   int  `json:"truncated_count"`
	BadIndexCount    int  `json:"bad_index_count"`
	FlowWindowCount  int  `json:"flow_window_count"`
	LossBudgetPassed bool `json:"loss_budget_passed"`
	KeyframeBudgetPassed bool `json:"keyframe_budget_passed"`
	Passed           bool `json:"passed"`
	Canceled         bool `json:"canceled"`
}

// Interval is one anomaly region with its threshold judgment and the
// configured-budget deviation that explains the verdict.
type Interval struct {
	Span             model.Span  `json:"span"`
	Kind             string      `json:"kind"`
	Threshold        string      `json:"threshold"`
	Observed         int64       `json:"observed"`
	BudgetLimit      int64       `json:"budget_limit"`
	Deviation        int64       `json:"deviation"`
	Passed           bool        `json:"passed"`
	Conclusions      []Conclusion `json:"conclusions"`
}

// Conclusion is the per-item processing verdict required by the acceptance
// criteria: every packet, index and region records what happened to it.
type Conclusion struct {
	Item     string           `json:"item"`
	Sequence uint32           `json:"sequence,omitempty"`
	Track    uint32           `json:"track,omitempty"`
	Span     model.Span       `json:"span"`
	Status   string           `json:"status"`
	Detail   string           `json:"detail,omitempty"`
	Codes    []model.ErrorCode `json:"codes,omitempty"`
}

// Report is the complete serialized investigation result.
type Report struct {
	Profile     string           `json:"profile"`
	Seed        int64            `json:"seed"`
	Summary     Summary          `json:"summary"`
	Intervals   []Interval       `json:"intervals"`
	Findings    model.Findings   `json:"findings"`
	Conclusions []Conclusion     `json:"conclusions"`
	Timeline    []model.TimelineEvent `json:"timeline,omitempty"`
}

// Builder accumulates the two analysis paths and renders one Report.
type Builder struct {
	budget   Budget
	profile  string
	seed     int64
	findings model.Findings
	anomalies []merge.Anomaly
	conclusions []Conclusion
	timeline []model.TimelineEvent
	flowWindows int
}

// NewBuilder creates a report builder with the configured budget.
func NewBuilder(profile string, seed int64, budget Budget) *Builder {
	return &Builder{budget: budget, profile: profile, seed: seed}
}

// AddFinding records one structured finding.
func (b *Builder) AddFinding(f model.Finding) { b.findings = append(b.findings, f) }

// AddFindings records a batch of findings.
func (b *Builder) AddFindings(fs model.Findings) { b.findings = append(b.findings, fs...) }

// AddConclusion records one per-item processing verdict.
func (b *Builder) AddConclusion(c Conclusion) { b.conclusions = append(b.conclusions, c) }

// AddAnomalies records merged anomaly regions.
func (b *Builder) AddAnomalies(as []merge.Anomaly) { b.anomalies = append(b.anomalies, as...) }

// SetTimeline attaches the merged deterministic timeline.
func (b *Builder) SetTimeline(events []model.TimelineEvent) { b.timeline = events }

// SetFlowWindowCount records how many throughput windows were measured.
func (b *Builder) SetFlowWindowCount(n int) { b.flowWindows = n }

// Build renders the final, deterministically sorted report.
func (b *Builder) Build(packets, tracks, keyframes, duplicates int, canceled bool) *Report {
	model.SortFindings(b.findings)
	lossCount := model.CountCode(b.findings, model.ErrSequenceGap)
	truncated := model.CountCode(b.findings, model.ErrTruncatedPacket)
	badIndex := model.CountCode(b.findings, model.ErrMissingIndex)

	lossPassed := lossCount <= b.budget.MaxLossPackets
	kfPassed := b.keyframeBudgetPassed()
	passed := lossPassed && kfPassed && !canceled

	r := &Report{
		Profile:  b.profile,
		Seed:     b.seed,
		Findings: b.findings,
		Conclusions: b.conclusions,
		Timeline: b.timeline,
		Summary: Summary{
			PacketCount:      packets,
			TrackCount:       tracks,
			KeyframeCount:    keyframes,
			LossCount:        lossCount,
			DuplicateCount:   duplicates,
			TruncatedCount:   truncated,
			BadIndexCount:    badIndex,
			FlowWindowCount:  b.flowWindows,
			LossBudgetPassed: lossPassed,
			KeyframeBudgetPassed: kfPassed,
			Passed:           passed,
			Canceled:         canceled,
		},
	}
	r.Intervals = b.buildIntervals()
	return r
}

func (b *Builder) keyframeBudgetPassed() bool {
	for _, f := range b.findings {
		if f.Code == model.ErrKeyframeGap {
			return false
		}
	}
	return true
}

func (b *Builder) buildIntervals() []Interval {
	var out []Interval
	// Loss intervals carry the loss-budget threshold and deviation.
	for _, a := range b.anomalies {
		limit := int64(b.budget.MaxLossPackets)
		observed := int64(a.LossCount)
		out = append(out, Interval{
			Span:        a.Span,
			Kind:        "loss_flow",
			Threshold:   "max_loss_packets",
			Observed:    observed,
			BudgetLimit: limit,
			Deviation:   observed - limit,
			Passed:      observed <= limit,
			Conclusions: b.spanConclusions(a.Span),
		})
	}
	// Keyframe gaps carry the keyframe interval budget.
	for _, f := range b.findings {
		if f.Code != model.ErrKeyframeGap {
			continue
		}
		limit := int64(b.budget.MaxKeyframeInterval)
		observed := int64(f.Span.Duration())
		out = append(out, Interval{
			Span:        f.Span,
			Kind:        "keyframe_gap",
			Threshold:   "max_keyframe_interval",
			Observed:    observed,
			BudgetLimit: limit,
			Deviation:   observed - limit,
			Passed:      observed <= limit,
			Conclusions: b.spanConclusions(f.Span),
		})
	}
	return out
}

func (b *Builder) spanConclusions(span model.Span) []Conclusion {
	var out []Conclusion
	for _, c := range b.conclusions {
		if c.Span.Overlaps(span) || span.Contains(c.Span.Start) {
			out = append(out, c)
		}
	}
	return out
}

// JSON serializes the report with stable key ordering from struct tags.
func (r *Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}
