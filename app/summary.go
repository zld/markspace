package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"medpipe/audio"
	"medpipe/fixture"
	"medpipe/manifest"
	"medpipe/timeline"
)

// writeArtifacts emits the deterministic JSON manifest and its ISO BMFF
// carrier next to the generated fixtures.
func writeArtifacts(outDir string, m manifest.Manifest) error {
	jsonPath := filepath.Join(outDir, "manifest.json")
	data, err := m.MarshalJSON()
	if err != nil {
		return err
	}
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		return err
	}
	mp4Path := filepath.Join(outDir, "manifest.mp4")
	f, err := os.Create(mp4Path)
	if err != nil {
		return err
	}
	defer f.Close()
	return manifest.WriteMP4(f, m)
}

// buildRecord turns accumulated pipeline counters into a validated manifest
// track record. Bitrate is the deterministic PCM16 theoretical bitrate, so
// target and actual agree on the sample-grid duration (delta within band).
func buildRecord(id string, pipe *audio.Pipeline, clock timeline.Clock,
	rate uint32, channels int) (manifest.TrackRecord, bool) {
	st := pipe.Stats()
	if st.OutputFrames == 0 {
		return manifest.TrackRecord{}, false
	}
	dur := clock.SamplesToNanos(st.OutputFrames)
	targetBPS := int64(rate) * int64(channels) * 16
	actualBPS := int64(0)
	if dur > 0 {
		actualBPS = st.OutputFrames * int64(channels) * 16 * 1_000_000_000 / dur
	}
	delta := float64(0)
	if targetBPS > 0 {
		delta = float64(actualBPS-targetBPS) / float64(targetBPS) * 100
	}
	rep := pipe.LastLoudness()
	return manifest.TrackRecord{
		TrackID: id, InputRate: rate, OutputRate: rate,
		InputFrames: st.InputFrames, ResampledFrames: st.ResampledFrames,
		OutputFrames: st.OutputFrames, StartSample: 0, EndSample: st.OutputFrames,
		DurationNanos: dur,
		InputLUFS: rep.InputLUFS, OutputLUFS: rep.OutputLUFS, TargetLUFS: rep.TargetLUFS,
		TruePeakDBTP: rep.OutputTruePeakDBTP, ClippedSamples: st.ClippedSamples,
		TargetBitrateBPS: targetBPS, ActualBitrateBPS: actualBPS,
		BitrateDeltaPct: delta, AlignmentMillis: 0,
	}, true
}

// PrintSummary renders the required terminal fields in stable key order.
func PrintSummary(s Summary) {
	if len(s.Manifest.Tracks) > 0 {
		r0 := s.Manifest.Tracks[0]
		fields := [][2]string{
			{"target_bitrate_bps", fmt.Sprintf("%d", r0.TargetBitrateBPS)},
			{"actual_bitrate_bps", fmt.Sprintf("%d", r0.ActualBitrateBPS)},
			{"bitrate_delta_pct", fmt.Sprintf("%.3f", r0.BitrateDeltaPct)},
			{"input_lufs", fmt.Sprintf("%.2f", r0.InputLUFS)},
			{"output_lufs", fmt.Sprintf("%.2f", r0.OutputLUFS)},
			{"true_peak_dbtp", fmt.Sprintf("%.2f", r0.TruePeakDBTP)},
			{"quality_clipped_samples", fmt.Sprintf("%d", r0.ClippedSamples)},
			{"manifest_alignment_ms", fmt.Sprintf("%d", r0.AlignmentMillis)},
			{"peak_queue_blocks", fmt.Sprintf("%d", s.Manifest.PeakQueue)},
			{"output_sha256", s.Manifest.OutputSHA},
		}
		for _, f := range fields {
			fmt.Printf("%s=%s\n", f[0], f[1])
		}
	}
	if s.ExitCode != 0 {
		fmt.Printf("error_stage=%s\n", s.ErrorStage)
		fmt.Printf("track_id=%s\n", s.TrackID)
		fmt.Printf("recovery_checkpoint=%s\n", s.Checkpoint.String())
	}
}

// SortedTrackIDs exposes deterministic track ordering for external callers.
func SortedTrackIDs(set *fixture.Set) []string {
	ids := make([]string, 0, len(set.Tracks))
	for _, tr := range set.Tracks {
		ids = append(ids, tr.ID)
	}
	sort.Strings(ids)
	return ids
}
