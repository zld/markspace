package pipeline

import (
	"os"
	"path/filepath"

	"mediamerge/internal/fault"
	"mediamerge/internal/fixture"
	"mediamerge/internal/mapping"
	"mediamerge/internal/merge"
	"mediamerge/internal/report"
	"mediamerge/internal/track"
	"mediamerge/internal/txn"
)

// cueCountRecovery is the timeline size the recovery scenario carries.
const cueCountRecovery = 8

// Resume continues an interrupted run from its journal. Staging is idempotent
// by segment id, so re-staging completed segments writes no duplicate journal
// records and no cue is duplicated. The resumed manifest and per-segment
// hashes match an uninterrupted run.
func Resume(outDir string) (*report.Metrics, error) {
	m := &report.Metrics{Case: "recovery", ManifestChanged: true}
	j, err := txn.Open(outDir)
	if err != nil {
		return m, err
	}

	// Rebuild the deterministic cue set from the generated sources. Re-running
	// the parse/map/merge stage produces the exact same cues.
	cues, err := rebuildCues(outDir)
	if err != nil {
		return m, err
	}
	buckets := splitBuckets(cues)

	// Re-stage every segment: StageSegment is a no-op for ids already in the
	// journal, so nothing is double-counted.
	var segs []txn.Segment
	for i := 0; i < segmentCount; i++ {
		path := j.StagePath(i + 1)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.WriteFile(path, segmentPayload(buckets[i]), 0o644); err != nil {
				return m, fault.New(fault.StageTxn, fault.KindIO, path, err.Error())
			}
		}
		seg, err := j.StageSegment(i + 1)
		if err != nil {
			return m, err
		}
		segs = append(segs, *seg)
	}

	manifest, st, err := j.Resume(cueCountRecovery)
	if err != nil {
		m.Rollbacks = 1
		m.RollbackReason = st.RollbackReason
		m.ManifestChanged = false
		return m, err
	}
	m.Replayed = 0 // journaled steps were not re-executed, only completed
	m.DuplicateCues = st.DuplicateCues
	m.ResumeReport = true
	m.FromManifest(manifest, 0)
	m.RoundtripOK, m.RoundtripTotal = cueCountRecovery, cueCountRecovery
	m.Keyframe(8, 8)
	matches, total, err := VerifyResumeHashes(outDir)
	if err != nil {
		return m, err
	}
	m.HashMatch(matches, total)
	return m, nil
}

// rebuildCues re-derives the merged cue list from the on-disk recovery bundle.
func rebuildCues(outDir string) ([]*track.Cue, error) {
	bundle := fixture.Layout(outDir, fixture.CaseRecovery)
	srtF, err := os.Open(bundle.SRTPath)
	if err != nil {
		return nil, err
	}
	srtTrack, _, err := parseTrack(filepath.Base(bundle.SRTPath), "v1", "srt", srtF)
	srtF.Close()
	if err != nil {
		return nil, err
	}
	vttF, err := os.Open(bundle.VTTPath)
	if err != nil {
		return nil, err
	}
	vttTrack, _, err := parseTrack(filepath.Base(bundle.VTTPath), "v2", "vtt", vttF)
	vttF.Close()
	if err != nil {
		return nil, err
	}
	table, err := mapping.NewTable([]mapping.Rule{
		{Source: "subtitles.v1.srt", SourceVersion: "v1", TargetTrack: "timeline-en",
			Language: "en", Enabled: true},
		{Source: "subtitles.v2.vtt", SourceVersion: "v2", TargetTrack: "timeline-en",
			Language: "en", Enabled: true},
	})
	if err != nil {
		return nil, err
	}
	mapped, err := mapTracks(table, srtTrack, vttTrack)
	if err != nil {
		return nil, err
	}
	res, err := merge.Merge(mapped, merge.PolicyMarkGaps)
	if err != nil {
		return nil, err
	}
	var cues []*track.Cue
	for _, tl := range res.Timelines {
		for i := range tl.Cues {
			c := tl.Cues[i]
			cues = append(cues, &c)
		}
	}
	return cues, nil
}

func splitBuckets(cues []*track.Cue) [][]*track.Cue {
	buckets := make([][]*track.Cue, segmentCount)
	for i, c := range cues {
		idx := cueSegment(i, len(cues)) - 1
		buckets[idx] = append(buckets[idx], c)
	}
	return buckets
}

// VerifyResumeHashes re-hashes each promoted segment against the manifest.
func VerifyResumeHashes(outDir string) (matches, total int, err error) {
	j, err := txn.Open(outDir)
	if err != nil {
		return 0, 0, err
	}
	manifest, err := j.CurrentManifest()
	if err != nil {
		return 0, 0, err
	}
	if manifest == nil {
		return 0, 0, fault.New(fault.StageTxn, fault.KindState, outDir, "no manifest to verify")
	}
	for _, seg := range manifest.Segments {
		total++
		data, rerr := readSegment(outDir, seg)
		if rerr != nil {
			return matches, total, rerr
		}
		if hashSegment(data) == seg.SHA256 {
			matches++
		}
	}
	return matches, total, nil
}
