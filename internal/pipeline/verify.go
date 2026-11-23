package pipeline

import (
	"os"

	"mediaroute/internal/clock"
	"mediaroute/internal/export"
	"mediaroute/internal/timeline"
)

// verifyManifestAgainstJournal is the export/commit consistency gate. Every
// exported frame must resolve to a journal-committed artifact whose on-disk
// bytes hash to the same digest the manifest recorded; segment digests are
// then recomputed from those bytes so segment_sha256_match is evidence, not
// an assertion. It returns the number of independently matched segments.
func (r *Runner) verifyManifestAgainstJournal(m *export.Manifest) (int, error) {
	// Recompute each exported frame's bytes from its committed artifact.
	for _, rec := range m.Records {
		art, ok := r.committedFrames[frameKey{track: rec.Track, rtpTS: rec.RTPTS}]
		if !ok || art.stage != rec.SegmentID {
			return 0, clock.E(clock.PhaseExport, rec.Track, -1, false,
				"exported frame has no committed journal artifact", nil)
		}
		raw, err := os.ReadFile(r.coord.CommittedPath(art.stage, art.key))
		if err != nil {
			return 0, clock.E(clock.PhaseExport, rec.Track, -1, false,
				"committed artifact unreadable during export verification", err)
		}
		sum := sha256Hex(raw)
		if sum != rec.FrameSHA || sum != art.sha {
			return 0, clock.E(clock.PhaseExport, rec.Track, -1, false,
				"exported frame digest differs from committed artifact", nil)
		}
	}
	recomputed, err := m.RecomputeSegmentHashes()
	if err != nil {
		return 0, err
	}
	matched := 0
	for _, seg := range m.Segments {
		if recomputed[seg.SegmentID] == seg.SHA256 &&
			len(seg.Frames) == seg.FrameCount {
			matched++
		}
	}
	return matched, nil
}

// checkCommittedOnly refuses any timeline frame whose segment id was not
// durably committed by the journal, so partial results cannot masquerade as
// committed output.
func (r *Runner) checkCommittedOnly(tl *timeline.Timeline) error {
	stages, err := r.coord.CommittedStages()
	if err != nil {
		return err
	}
	live := make(map[string]bool, len(stages))
	for _, s := range stages {
		live[s] = true
	}
	for _, fr := range tl.Frames {
		if fr.SegmentID == "" || !live[fr.SegmentID] {
			return clock.E(clock.PhaseTimeline, fr.Track, -1, false,
				"timeline frame references an uncommitted segment", nil)
		}
	}
	return nil
}
