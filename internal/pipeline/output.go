package pipeline

import (
	"fmt"
	"strings"

	"mediascan/internal/model"
)

func dedupeFaults(fs []model.Fault) []model.Fault {
	seen := map[string]bool{}
	out := make([]model.Fault, 0, len(fs))
	for _, f := range fs {
		key := strings.Join([]string{
			f.Code, f.Segment, f.BoxType, f.Message,
			fmt.Sprintf("%d", f.OffsetBytes),
			fmt.Sprintf("%d", f.TimeStart),
			fmt.Sprintf("%d", f.SampleIndex),
		}, "|")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	return out
}

// KVText renders the stable terminal key=value block. The required acceptance
// tokens are always present, even when their value is unknown (-1).
func KVText(res *Result) string {
	r := res.Report
	var b strings.Builder
	status := "ok"
	if res.ExitCode == 2 {
		status = "faults_located"
	}
	sha := ""
	gopMin, gopMax := int64(model.Unknown), int64(model.Unknown)
	input, kind := "", ""
	var segCount, samples int
	if r != nil {
		sha = r.SHA256
		gopMin, gopMax = r.GOPMinTicks, r.GOPMaxTicks
		input, kind = r.Input, r.InputKind
		segCount, samples = r.SegmentCount, int(r.TotalSamples)
	}
	fmt.Fprintf(&b, "status=%s\n", status)
	fmt.Fprintf(&b, "exit_code=%d\n", res.ExitCode)
	fmt.Fprintf(&b, "input=%s\n", input)
	fmt.Fprintf(&b, "input_kind=%s\n", kind)
	fmt.Fprintf(&b, "segments=%d\n", segCount)
	fmt.Fprintf(&b, "total_samples=%d\n", samples)
	fmt.Fprintf(&b, "fault_offset_bytes=%d\n", res.PrimaryOffset)
	fmt.Fprintf(&b, "fault_time_ticks=%d\n", res.PrimaryTime)
	fmt.Fprintf(&b, "gop_min_ticks=%d\n", gopMin)
	fmt.Fprintf(&b, "gop_max_ticks=%d\n", gopMax)
	fmt.Fprintf(&b, "resume_from_sample=%d\n", res.ResumeFromSample)
	fmt.Fprintf(&b, "elapsed_ms=%d\n", res.ElapsedMS)
	fmt.Fprintf(&b, "memory_peak_bytes=%d\n", res.MemoryPeakBytes)
	fmt.Fprintf(&b, "sha256=%s\n", sha)
	fmt.Fprintf(&b, "wrap_events=%d\n", res.WrapEvents)
	fmt.Fprintf(&b, "drift_ppm=%d\n", res.DriftPPM)
	if res.CheckpointSource != "" {
		fmt.Fprintf(&b, "checkpoint_source=%s\n", res.CheckpointSource)
	}
	if res.RollbackReason != "" {
		fmt.Fprintf(&b, "checkpoint_rollback_reason=%q\n", res.RollbackReason)
	}
	return b.String()
}

// FaultTable renders a deterministic per-fault listing.
func FaultTable(res *Result) string {
	if res.Report == nil || len(res.Report.Faults) == 0 {
		return "faults=none\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "faults=%d\n", len(res.Report.Faults))
	for _, f := range res.Report.Faults {
		fmt.Fprintf(&b,
			"- code=%s severity=%s segment=%s offset=%d time=[%d,%d] sample=%d box=%s msg=%q\n",
			f.Code, f.Severity, f.Segment, f.PrimaryOffset(), f.TimeStart, f.TimeEnd,
			f.SampleIndex, f.BoxType, f.Message)
	}
	return b.String()
}
