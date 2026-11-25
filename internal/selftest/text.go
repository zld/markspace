package selftest

import (
	"fmt"
	"strings"
)

// FormatText renders terminal-friendly key=value verification output.
func FormatText(r *Report) string {
	var b strings.Builder
	status := "PASS"
	if !r.OK {
		status = "FAIL"
	}
	fmt.Fprintf(&b, "mediacore selftest: %s\n", status)
	fmt.Fprintf(&b, "work_dir=%s\n", r.WorkDir)
	fmt.Fprintf(&b, "target_bitrate=%d\n", r.TargetBitrate)
	fmt.Fprintf(&b, "actual_bitrate=%.2f\n", r.ActualBitrate)
	fmt.Fprintf(&b, "bitrate_delta_pct=%.4f\n", r.BitrateDeltaPct)
	fmt.Fprintf(&b, "video_psnr=%s\n", r.VideoPSNR)
	fmt.Fprintf(&b, "audio_diff_samples=%d\n", r.AudioDiffSamples)
	fmt.Fprintf(&b, "manifest_alignment_ms=%.4f\n", r.ManifestAlignmentMS)
	fmt.Fprintf(&b, "duplicates_removed=%d\n", r.DuplicatesRemoved)
	fmt.Fprintf(&b, "reordered_packets=%d\n", r.ReorderedPackets)
	fmt.Fprintf(&b, "missing_packets=%d\n", r.MissingPackets)
	fmt.Fprintf(&b, "index_rebuilt=%d\n", r.IndexRebuilt)
	fmt.Fprintf(&b, "normal_missing_packets=%d\n", r.NormalMissingPackets)
	fmt.Fprintf(&b, "lossy_missing_packets=%d\n", r.LossyMissingPackets)
	fmt.Fprintf(&b, "repeat_manifest_digest_match=%t\n", r.RepeatManifestDigestMatch)

	for _, c := range r.Cases {
		if c == nil {
			continue
		}
		fmt.Fprintf(&b, "case=%s ok=%t", c.Name, c.OK)
		if c.Detail != "" {
			fmt.Fprintf(&b, " detail=%q", c.Detail)
		}
		if c.ExpectedErrorCode != "" {
			fmt.Fprintf(&b, " expected_error=%s", c.ExpectedErrorCode)
		}
		if c.ManifestPath != "" {
			fmt.Fprintf(&b, " manifest=%s", c.ManifestPath)
		}
		if c.ManifestSHA256 != "" {
			fmt.Fprintf(&b, " manifest_sha256=%s", c.ManifestSHA256[:min(16, len(c.ManifestSHA256))])
		}
		if c.OutputSHA256 != "" {
			fmt.Fprintf(&b, " output_sha256=%s", c.OutputSHA256[:min(16, len(c.OutputSHA256))])
		}
		fmt.Fprintf(&b, " duplicates=%d reordered=%d missing=%d index_rebuilt=%d gaps=%d\n",
			c.Recovery.DuplicatesRemoved, c.Recovery.ReorderedPackets,
			c.Recovery.MissingPackets, c.Recovery.IndexRebuilt, c.Gaps)
	}
	for _, e := range r.Errors {
		fmt.Fprintf(&b, "error=%q\n", e)
	}
	return b.String()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
