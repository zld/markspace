package pipeline

import "fmt"

// MetricLines renders the stable terminal key/value acceptance output.
func MetricLines(r *Result) []string {
	var lines []string
	if r.Timeline != nil {
		lines = append(lines, fmt.Sprintf("sync_max_us=%d", r.Timeline.SyncMaxNS/1000))
	}
	if r.RTP != nil {
		lossDen := r.RTP.Recovered + r.RTP.Unrecoverable
		lines = append(lines, fmt.Sprintf("recovered=%d/%d", r.RTP.Recovered, lossDen))
		lines = append(lines, fmt.Sprintf("recovery_rate=%.0f%%", r.RTP.RecoveryRate))
	}
	if r.Render != nil {
		lines = append(lines, fmt.Sprintf("p95_ms=%.3f", r.Render.P95MS))
		lines = append(lines, fmt.Sprintf("frames=%d", len(r.Render.Frames)))
		for _, f := range r.Render.Frames {
			lines = append(lines, fmt.Sprintf("frame_hash sequence=%d pts_ns=%d sha256=%s", f.Sequence, f.PTSNS, f.SHA256))
		}
	}
	if r.Journal != nil {
		lines = append(lines, fmt.Sprintf("recovered_artifacts=%d", r.Journal.RecoveredArtifacts))
		lines = append(lines, fmt.Sprintf("published=%d", r.Journal.Published))
		lines = append(lines, fmt.Sprintf("duplicates=%d", r.Journal.Duplicates))
	}
	if r.Migration != nil {
		lines = append(lines, fmt.Sprintf("rollback_checkpoint=%d", r.Migration.Checkpoints))
	}
	return lines
}
