package rtpreplay

import (
	"fmt"
	"strings"
)

// FormatSummary renders the terminal acceptance view. Field names are stable
// and match the report keys so a human can diff stdout against the JSON.
func FormatSummary(opts RunOptions, pt *ParsedTrace, res *RunResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "case=%s seed=%d depth=%d\n", opts.Case, opts.Seed, opts.Depth)
	fmt.Fprintf(&b, "input_digest=%s\n", pt.InputDigest)
	status := "ok"
	exitCode := 0
	if !res.Completed {
		status = "failed"
		exitCode = 1
	}
	fmt.Fprintf(&b, "status=%s\nexit_code=%d\n", status, exitCode)
	fmt.Fprintf(&b, "artifact_sha256=%s\n", res.ArtifactDigest)
	for _, r := range res.Results {
		line := fmt.Sprintf("node_%s=%s attempts=%d", r.Node, r.State, r.Attempts)
		if r.BlockedBy != "" {
			line += fmt.Sprintf(" blocked_by=%s", r.BlockedBy)
			if r.Reason != "" {
				line += fmt.Sprintf(" reason=%q", r.Reason)
			}
		}
		if r.Error != nil {
			line += fmt.Sprintf(" error_code=%s", r.Error.Code)
		}
		fmt.Fprintln(&b, line)
	}
	if res.FirstBlocker != nil {
		b.WriteString(formatBlocker(res.FirstBlocker))
	}
	if !res.Completed {
		return b.String()
	}
	metricsArt := findArtifact(res, "metrics")
	manifestArt := findArtifact(res, "manifest")
	reportArt := findArtifact(res, "report")
	if metricsArt == nil || reportArt == nil {
		fmt.Fprintln(&b, "status=incomplete")
		return b.String()
	}
	report := reportArt.Value.(*Report)
	metrics := metricsArt.Value.([]*Metrics)
	var manifest *Manifest
	if manifestArt != nil {
		manifest = manifestArt.Value.(*Manifest)
	}
	for _, m := range metrics {
		fmt.Fprintf(&b, "track=%s codec=%s timebase=%s\n", m.Track, m.Codec, m.Timebase)
		fmt.Fprintf(&b, "  target_bitrate_bps=%d\n", m.Bitrate.TargetBps)
		fmt.Fprintf(&b, "  actual_bitrate_bps=%d\n", m.Bitrate.ActualBps)
		fmt.Fprintf(&b, "  bitrate_deviation_pct=%.4f\n", m.Bitrate.DeviationPct)
		fmt.Fprintf(&b, "  quality_score=%.2f\n", m.QualityScore)
		fmt.Fprintf(&b, "  loss_packets=%d\n", m.LossPackets)
		fmt.Fprintf(&b, "  duplicate_packets=%d\n", m.DuplicatePackets)
		fmt.Fprintf(&b, "  reordered_packets=%d\n", m.ReorderedPackets)
		fmt.Fprintf(&b, "  latency_p50_ms=%.3f\n", m.Latency.P50Ms)
		fmt.Fprintf(&b, "  latency_p90_ms=%.3f\n", m.Latency.P90Ms)
		fmt.Fprintf(&b, "  latency_p99_ms=%.3f\n", m.Latency.P99Ms)
		fmt.Fprintf(&b, "  loss_pct=%.4f\n", m.LossPct)
		fmt.Fprintf(&b, "  burst_loss_runs=%d\n", len(m.BurstLossRuns))
		for i, run := range m.BurstLossRuns {
			fmt.Fprintf(&b, "  gap[%d]=start_ext_seq:%d end_ext_seq:%d count:%d\n", i, run.StartExtSeq, run.EndExtSeq, run.Count)
		}
		fmt.Fprintf(&b, "  quality_method=%q\n", m.QualityInput.Method)
		fmt.Fprintf(&b, "  units=bitrate:%s latency:%s quality:%s\n", UnitBitsPerSec, UnitMilliseconds, m.QualityInput.Unit)
	}
	fmt.Fprintf(&b, "manifest_alignment_error_ms=%.3f\n", manifest.AlignmentErrorMs)
	for _, e := range manifest.Entries {
		fmt.Fprintf(&b, "manifest_entry=%s start_ms=%d end_ms=%d packets=%d first_ext_seq=%d last_ext_seq=%d gaps=%d status=%s alignment_error_ms=%.3f\n",
			e.Track, e.StartMs, e.EndMs, e.PacketCount, e.FirstExtSeq, e.LastExtSeq, e.GapCount, e.Status, e.AlignmentErrorMs)
	}
	fmt.Fprintf(&b, "report_status=%s report_exit_code=%d\n", report.Status, report.ExitCode)
	fmt.Fprintf(&b, "config_digest=%s\n", report.ConfigDigest)
	return b.String()
}

func formatBlocker(r *NodeResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "first_blocker=%s\n", r.Node)
	fmt.Fprintf(&b, "first_blocker_state=%s\n", r.State)
	fmt.Fprintf(&b, "first_blocker_blocked_by=%s\n", r.BlockedBy)
	if r.Reason != "" {
		fmt.Fprintf(&b, "first_blocker_reason=%q\n", r.Reason)
	}
	if r.Error != nil {
		fmt.Fprintf(&b, "first_blocker_error_code=%s\n", r.Error.Code)
		fmt.Fprintf(&b, "first_blocker_error=%q\n", r.Error.Error())
		fmt.Fprintf(&b, "first_blocker_retryable=%t\n", r.Error.Retryable)
	}
	return b.String()
}
