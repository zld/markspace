package pipeline

import (
	"os"
	"path/filepath"
	"sort"

	"archivecheck/internal/manifest"
	"archivecheck/internal/media"
	"archivecheck/internal/merr"
	"archivecheck/internal/task"
	"archivecheck/internal/timeline"
)

func recoverKey(trackID, seq uint32) string {
	return "recover:t" + itoa(uint64(trackID)) + ":s" + itoa(uint64(seq))
}

// composeArtifacts turns per-segment recoveries into timeline inputs and
// builds the unified integer clock timeline.
func composeArtifacts(pl *manifest.Playlist, specs []media.TrackSpec,
	art *artifacts) error {

	inputs := make([]timeline.ComposeInput, 0, len(specs))
	for _, spec := range specs {
		clk, err := timeline.NewClock(spec.ID, spec.TimebaseNum, spec.TimebaseDen)
		if err != nil {
			return err
		}
		in := timeline.ComposeInput{
			Spec: spec, Clock: clk,
			SegmentLost:  map[uint32]int{},
			SegmentBytes: map[uint32]int{},
			SegmentTrunc: map[uint32]bool{},
		}
		for seq, rec := range art.recovery[spec.ID] {
			in.Frames = append(in.Frames, rec.Frames...)
			in.SegmentLost[seq] = rec.LostPackets
			in.SegmentBytes[seq] = rec.MediaBytes
			in.SegmentTrunc[seq] = rec.Truncated
		}
		inputs = append(inputs, in)
	}
	tl, err := timeline.Compose(pl, inputs)
	if err != nil {
		return err
	}
	art.timeline = tl
	return nil
}

// fillReport populates the quantitative command-line report. Blocked
// runs still report every statistic that could be computed.
func fillReport(out *RunResult, pl *manifest.Playlist, art *artifacts,
	res *task.Result) {

	targets := map[uint32]int{}
	var targetSum int
	for _, t := range pl.Tracks {
		targets[t.ID] = t.TargetBitrateBps
		targetSum += t.TargetBitrateBps
	}
	if len(pl.Tracks) > 0 {
		out.TargetBitrateKbps = targetSum / len(pl.Tracks) / 1000
	}
	if art.timeline != nil {
		rows, summary := qualityMeasure(art.timeline.Segments, targets)
		_ = rows
		out.ActualBitrateKbps = summary.ActualKbps
		out.DeviationPct = bpToPercent(summary.DeviationBP)
		out.MeanDeviationPct = bpToPercent(summary.MeanDevBP)
		out.MaxDeviationPct = bpToPercent(summary.MaxDevBP)
		out.SampleCount = summary.Samples
		out.ManifestAlignMs = microsToMs(art.timeline.Align.ManifestMicros)
	}
	if art.stats.TotalFrames > 0 || art.timeline != nil {
		out.DecodedFrames = art.stats.DecodedFrames
		out.CorruptFrames = art.stats.CorruptFrames
		out.MeanLuma = art.stats.MeanLuma
	}
	if art.frames != nil {
		out.FrameCount = art.frames.Count
		out.FrameHashes = art.frames.Hashes()
	}
	affected := map[uint32]bool{}
	truncated := 0
	for trackID, segs := range art.recovery {
		for _, rec := range segs {
			out.LostPackets += rec.LostPackets
			out.DuplicatePackets += rec.DuplicatePkts
			if rec.Truncated {
				truncated++
			}
			if rec.LostPackets > 0 || rec.Truncated {
				affected[trackID] = true
			}
		}
	}
	out.TruncatedSegments = truncated
	for id := range affected {
		out.AffectedTracks = append(out.AffectedTracks, id)
	}
	sort.Slice(out.AffectedTracks, func(i, j int) bool {
		return out.AffectedTracks[i] < out.AffectedTracks[j]
	})
}

func successfulKeys(res *task.Result) []string {
	var keys []string
	for key, st := range res.States {
		if st.State == task.StatusSuccess {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func readState(dir string) (*runState, error) {
	path := filepath.Join(dir, stateFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, merr.Wrap(err, "pipeline", path,
			"cannot read resume state; prior run directory is invalid")
	}
	var st runState
	if err := jsonUnmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func writeState(outDir string, st runState) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return merr.Wrap(err, "pipeline", outDir, "cannot create output directory")
	}
	path := filepath.Join(outDir, stateFile)
	tmp := path + ".tmp"
	data, err := jsonMarshalIndent(st)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return merr.Wrap(err, "pipeline", tmp, "state write failed")
	}
	if err := os.Rename(tmp, path); err != nil {
		return merr.Wrap(err, "pipeline", path, "state atomic commit failed")
	}
	return nil
}
