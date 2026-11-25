// Package report renders the auditable processing result: business threshold
// verdicts, byte-budget deltas, abnormal intervals and per-item conclusions.
// Exit codes are fixed: 0 all passed, 1 parse/runtime error, 2 threshold
// failure.
package report

import (
	"fmt"
	"strings"

	"packcheck/internal/model"
)

// Exit code semantics.
const (
	ExitOK        = 0
	ExitRuntime   = 1
	ExitThreshold = 2
)

// Threshold names the supported business thresholds.
type Threshold string

// Declared thresholds.
const (
	ThresholdBudgetBytes Threshold = "budget-bytes"
)

// LayerConclusion is the per-representation line in the report.
type LayerConclusion struct {
	Representation string
	ContainerOK    bool
	SubtitleOK     bool
	BoundaryOK     bool
	Verdict        string
	InManifest     bool
}

// AnomalyInterval is one abnormal time interval with its source offset.
type AnomalyInterval struct {
	Representation string
	Code           model.Code
	ByteOffset     int64
	StartTS        uint32
	EndTS          uint32
}

// Result is everything the acceptance harness prints and evaluates.
type Result struct {
	LayerCount          int
	AlignedBoundaries   bool
	InputBytes          int64
	OutputBytes         int64
	BudgetDeltaBytes    int64
	BudgetThreshold     int64
	ThresholdPass       bool
	RuntimeError        bool
	PeakQueueDepth      int
	Conclusions         []LayerConclusion
	Anomalies           []AnomalyInterval
	Issues              []model.Issue
	ManifestDigest      string
	ManifestText        string
}

// BudgetDelta computes input minus output, as required by the specification.
func BudgetDelta(inputBytes, outputBytes int64) int64 {
	return inputBytes - outputBytes
}

// EvaluateBudget marks the threshold failed when the absolute delta exceeds
// the allowance.
func (r *Result) EvaluateBudget(allowance int64) {
	r.BudgetThreshold = allowance
	r.ThresholdPass = abs64(r.BudgetDeltaBytes) <= allowance
}

// ExitCode maps the result onto the fixed process exit code.
func (r *Result) ExitCode() int {
	switch {
	case r.RuntimeError:
		return ExitRuntime
	case !r.ThresholdPass:
		return ExitThreshold
	default:
		return ExitOK
	}
}

// Render produces the stable key=value terminal output used for shell checks.
// Every field prints a number or an explicit verdict.
func (r *Result) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "layer_count=%d\n", r.LayerCount)
	fmt.Fprintf(&b, "aligned_boundaries=%t\n", r.AlignedBoundaries)
	fmt.Fprintf(&b, "input_bytes=%d\n", r.InputBytes)
	fmt.Fprintf(&b, "output_bytes=%d\n", r.OutputBytes)
	fmt.Fprintf(&b, "budget_delta_bytes=%d\n", r.BudgetDeltaBytes)
	fmt.Fprintf(&b, "budget_threshold_bytes=%d\n", r.BudgetThreshold)
	fmt.Fprintf(&b, "threshold=%s\n", passWord(r.ThresholdPass))
	fmt.Fprintf(&b, "peak_queue_depth=%d\n", r.PeakQueueDepth)
	fmt.Fprintf(&b, "manifest_digest=%s\n", r.ManifestDigest)
	for _, c := range r.Conclusions {
		fmt.Fprintf(&b, "layer name=%s container=%s subtitle=%s boundary=%s verdict=%s in_manifest=%t\n",
			c.Representation, passWord(c.ContainerOK), passWord(c.SubtitleOK),
			passWord(c.BoundaryOK), c.Verdict, c.InManifest)
	}
	for _, a := range r.Anomalies {
		fmt.Fprintf(&b, "anomaly representation=%s code=%s byte_offset=%d rtp_start=%d rtp_end=%d\n",
			a.Representation, a.Code, a.ByteOffset, a.StartTS, a.EndTS)
	}
	for _, i := range r.Issues {
		fmt.Fprintf(&b, "issue %s\n", i.String())
	}
	if r.ManifestText != "" {
		b.WriteString("manifest:\n")
		b.WriteString(r.ManifestText)
	}
	fmt.Fprintf(&b, "exit=%d\n", r.ExitCode())
	return b.String()
}

func passWord(ok bool) string {
	if ok {
		return "pass"
	}
	return "fail"
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
