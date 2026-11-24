package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"mediadag/internal/gen"
	"mediadag/internal/task"
)

// printHeader identifies the sample under test.
func (dm *demo) printHeader(name string, s *gen.Sample) {
	fmt.Fprintf(dm.out, "scenario=%s seed=%d input_digest=%s\n", name, dm.args.seed, s.Digest)
}

func (dm *demo) printVersions() {
	fmt.Fprintf(dm.out, "versions=mediadag/%s go-mp4/v1.3.0 pion-rtp/v1.8.11 pion-rtcp/v1.2.15\n",
		nodeVersion)
}

// printSummary emits the acceptance fields for a completed analysis.
func (dm *demo) printSummary(s *Summary, reused int, firstBlocked string) {
	fmt.Fprintf(dm.out, "target_kbps=%.0f actual_kbps=%.1f sent_kbps=%.1f bitrate_deviation_pct=%.2f\n",
		s.TargetKbps, s.ActualKbps, s.SentKbps, s.BitrateDevPct)
	fmt.Fprintf(dm.out, "loss_ratio=%.3f deviation_cause=%s\n", s.LossRatio, s.DeviationCause)
	fmt.Fprintf(dm.out, "packets_received=%d loss_detected=%d duplicates=%d late=%d\n",
		s.PacketsReceived, s.LossDetected, s.Duplicates, s.Late)
	fmt.Fprintf(dm.out, "idr_count=%d sps_count=%d pps_count=%d keyframe_interval_avg_ms=%.1f\n",
		s.IDRCount, s.SPSCount, s.PPSCount, s.KeyframeIntAvgMs)
	fmt.Fprintf(dm.out, "corrupt_ranges=%d manifest_alignment_ms=%.3f manifest_range_ms=[%.0f,%.0f] segments=%d\n",
		s.CorruptRanges, s.ManifestAlignMs, s.ManifestStartMs, s.ManifestEndMs, s.Segments)
	fmt.Fprintf(dm.out, "quality_score=%.2f resume_reused_nodes=%d\n", s.QualityScore, reused)
	fmt.Fprintf(dm.out, "first_blocked_dependency=%s\n", firstBlocked)
	fmt.Fprintf(dm.out, "result_sha256=%s\n", digestOfSummary(s))
}

// printStates lists persisted node outcomes with retry counts.
func (dm *demo) printStates(rep *task.Report) {
	names := make([]string, 0, len(rep.States))
	for n := range rep.States {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		st := rep.States[n]
		fmt.Fprintf(dm.out, "node=%s status=%s retries=%d category=%s digest=%s\n",
			n, st.Status, st.Retries, st.Category, shortDigest(st.Digest))
	}
}

func shortDigest(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func digestOfSummary(s *Summary) string {
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// acceptanceOK checks the numeric contracts of the acceptance demo.
func acceptanceOK(s *Summary) bool {
	return s.BitrateDevPct <= 8 &&
		s.CorruptRanges >= 1 &&
		s.ManifestAlignMs <= 5 &&
		s.QualityScore >= 0 && s.QualityScore <= 100
}
