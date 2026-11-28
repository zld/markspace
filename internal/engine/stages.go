package engine

import (
	"sort"
	"time"

	"mediaregress/internal/checkpoint"
	"mediaregress/internal/fingerprint"
	"mediaregress/internal/media"
	"mediaregress/internal/provenance"
	"mediaregress/internal/silence"
	"mediaregress/internal/split"
	"mediaregress/internal/timeline"
	"mediaregress/internal/track"
)

// stageStats counts reuse for the CLI report. Reuse is counted once per
// segment even when both its split result and its fingerprint are reused.
type stageStats struct {
	reusedIDs    map[string]bool
	reuseByTrack map[string]int
	computed     int
	complete     bool
}

func newStageStats() stageStats {
	return stageStats{reusedIDs: map[string]bool{}, reuseByTrack: map[string]int{}, complete: true}
}

func (s *stageStats) reuse(ids ...string) {
	for _, id := range ids {
		s.reusedIDs[id] = true
	}
}

func (s stageStats) reused() int { return len(s.reusedIDs) }

// produceSegments splits each track, reusing checkpointed segments whose
// window is already fully completed. Reuse is per track and per interval, so a
// damaged or changed track only costs its own work.
func produceSegments(opts Options, tl *timeline.Timeline, g *track.Graph, cp *checkpoint.State, mode Mode) ([]split.Segment, stageStats, error) {
	stats := newStageStats()
	var all []split.Segment
	for _, tt := range tl.Tracks {
		item := track.Item{Track: tt.TrackID, Node: track.NodeSplit}
		stored := cp.Track(tt.TrackID)
		var reused []split.Segment
		if mode == ModeRun && stored != nil && stored.HasStage(media.StageSplit) {
			for _, s := range stored.Segments {
				if s.Track != tt.TrackID {
					continue
				}
				if stored.Completed(s.Window.Start, s.Window.End) {
					reused = append(reused, s)
				}
			}
		}
		if len(reused) > 0 {
			if err := g.Begin(item); err != nil {
				return nil, stats, err
			}
			if err := g.Complete(item); err != nil {
				return nil, stats, err
			}
			ids := make([]string, 0, len(reused))
			for _, s := range reused {
				ids = append(ids, s.ID)
			}
			stats.reuse(ids...)
			if err := recordSegments(cp, tt.TrackID, reused); err != nil {
				return nil, stats, err
			}
			all = append(all, reused...)
			continue
		}
		if err := g.Begin(item); err != nil {
			return nil, stats, err
		}
		segs, err := split.Track(tt, opts.SegmentTarget)
		if err != nil {
			g.Fail(item, err)
			stats.complete = false
			continue
		}
		if err := split.Validate(segs); err != nil {
			g.Fail(item, err)
			stats.complete = false
			continue
		}
		if err := g.Complete(item); err != nil {
			return nil, stats, err
		}
		stats.computed += len(segs)
		if err := recordSegments(cp, tt.TrackID, segs); err != nil {
			return nil, stats, err
		}
		// Segmenting a track is complete work for its whole span; recording
		// the interval is what lets a resume trust the stored segments.
		if st := cp.Track(tt.TrackID); st != nil {
			st.AddStage(media.StageSplit)
			if tt.End > tt.Start {
				st.AddInterval(tt.Start, tt.End)
			}
		}
		all = append(all, segs...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Track != all[j].Track {
			return all[i].Track < all[j].Track
		}
		return all[i].Index < all[j].Index
	})
	return all, stats, nil
}

func recordSegments(cp *checkpoint.State, id string, segs []split.Segment) error {
	st := cp.Track(id)
	if st == nil {
		return media.Errorf(media.ClassCheckpoint, media.StageCheckpoint, id, -1, "checkpoint track missing")
	}
	for _, s := range segs {
		exists := false
		for _, have := range st.Segments {
			if have.ID == s.ID {
				exists = true
				break
			}
		}
		if !exists {
			st.Segments = append(st.Segments, s)
		}
	}
	return nil
}

// produceSilence runs silence detection for audio tracks, reusing stored
// ranges when the whole track's silence stage already completed.
func produceSilence(opts Options, tl *timeline.Timeline, segs []split.Segment, g *track.Graph, cp *checkpoint.State, mode Mode) ([]silence.Result, []*media.Error, error) {
	var results []silence.Result
	var errs []*media.Error
	needDetect := false
	for _, tt := range tl.Tracks {
		if tt.Kind != track.KindAudio {
			continue
		}
		item := track.Item{Track: tt.TrackID, Node: track.NodeSilence}
		stored := cp.Track(tt.TrackID)
		if mode == ModeRun && stored != nil && stored.HasStage(media.StageSilence) {
			if err := g.Begin(item); err != nil {
				return nil, nil, err
			}
			if err := g.Complete(item); err != nil {
				return nil, nil, err
			}
			results = append(results, silence.Result{
				Track: tt.TrackID, Silent: stored.Silence, Threshold: opts.SilenceThreshold,
				ParseVersion: tt.ParseVersion,
			})
			continue
		}
		needDetect = true
	}
	if !needDetect {
		sort.Slice(results, func(i, j int) bool { return results[i].Track < results[j].Track })
		return results, nil, nil
	}
	detected, derrs, err := silence.Detect(opts.Input, tl, segs, opts.SilenceWindow, opts.SilenceThreshold)
	if err != nil {
		return nil, nil, err
	}
	errs = append(errs, derrs...)
	byTrack := map[string]silence.Result{}
	for _, r := range detected {
		byTrack[r.Track] = r
	}
	for _, tt := range tl.Tracks {
		if tt.Kind != track.KindAudio {
			continue
		}
		r, ok := byTrack[tt.TrackID]
		if !ok {
			continue
		}
		item := track.Item{Track: tt.TrackID, Node: track.NodeSilence}
		if err := g.Begin(item); err != nil {
			return nil, nil, err
		}
		if err := g.Complete(item); err != nil {
			return nil, nil, err
		}
		if st := cp.Track(tt.TrackID); st != nil && !r.Truncated {
			st.Silence = r.Silent
			st.AddStage(media.StageSilence)
		}
		results = append(results, r)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Track < results[j].Track })
	return results, errs, nil
}

// produceFingerprints hashes segments not already recorded. Stored digests are
// reused but duplicate flags are recomputed across the full segment set, so a
// resumed run agrees with an uninterrupted one.
func produceFingerprints(opts Options, tl *timeline.Timeline, segs []split.Segment, g *track.Graph, cp *checkpoint.State, mode Mode, store *checkpoint.Store) ([]fingerprint.Result, []*media.Error, stageStats, error) {
	stats := newStageStats()
	storedByID := map[string]fingerprint.Result{}
	if mode == ModeRun {
		// Any durable per-segment result can be reused, even when the track's
		// fingerprint stage never finished. That is what makes a resume after
		// an interruption recompute only the missing intervals.
		for _, tr := range cp.Tracks {
			for _, r := range tr.Fingerprints {
				if r.Status != fingerprint.StatusComplete && r.Status != fingerprint.StatusIncomplete {
					continue
				}
				// The stored result is only valid for the same input bytes.
				if tr.InputDigest == "" || tr.InputDigest != cp.Track(r.Track).InputDigest {
					continue
				}
				storedByID[r.SegmentID] = r
			}
		}
	}
	var pending []split.Segment
	for _, s := range segs {
		if _, ok := storedByID[s.ID]; ok {
			stats.reuse(s.ID)
			stats.reuseByTrack[s.Track]++
			continue
		}
		pending = append(pending, s)
	}
	results := make([]fingerprint.Result, 0, len(segs))
	for _, s := range segs {
		if r, ok := storedByID[s.ID]; ok {
			// Keep the stored digest and byte count, drop derived flags.
			r.Duplicate = false
			r.DuplicateOf = ""
			results = append(results, r)
		}
	}
	if len(pending) > 0 {
		fresh, errs, err := fingerprint.Compute(opts.Input, tl, pending, opts.MinCompare)
		if err != nil {
			return nil, nil, stats, err
		}
		results = append(results, fresh...)
		// Record fresh results per track, one durable step per segment so an
		// interruption loses at most the segment in flight.
		byTrack := map[string][]fingerprint.Result{}
		computed := 0
		for _, r := range fresh {
			byTrack[r.Track] = append(byTrack[r.Track], r)
			if r.Status == fingerprint.StatusUnreadable {
				continue
			}
			computed++
			if st := cp.Track(r.Track); st != nil {
				st.Fingerprints = mergeFingerprints(st.Fingerprints, []fingerprint.Result{r})
				st.AddInterval(r.Window.Start, r.Window.End)
			}
			if opts.InterruptAfterSegments > 0 && computed >= opts.InterruptAfterSegments {
				cp.Stage = media.StageFingerprint
				cp.Normalize()
				if err := store.Save(cp); err != nil {
					return nil, nil, stats, err
				}
				return nil, nil, stats, media.Errorf(media.ClassCheckpoint, media.StageFingerprint, "",
					-1, "simulated interruption after %d fingerprinted segments", computed)
			}
		}
		_ = errs
	}
	// All fingerprint nodes complete only when every segment for the track
	// has a comparable result.
	allErrs := []*media.Error{}
	byTrackSeg := map[string]int{}
	for _, s := range segs {
		byTrackSeg[s.Track]++
	}
	byTrackRes := map[string]int{}
	for _, r := range results {
		if r.Status != fingerprint.StatusUnreadable {
			byTrackRes[r.Track]++
		}
	}
	for _, tt := range tl.Tracks {
		item := track.Item{Track: tt.TrackID, Node: track.NodeFingerprint}
		if byTrackSeg[tt.TrackID] != byTrackRes[tt.TrackID] {
			stats.complete = false
			if g.Ready(item) == nil {
				_ = g.Begin(item)
				g.Fail(item, media.NewError(media.ClassCorruptInput, media.StageFingerprint, tt.TrackID,
					-1, "some segments could not be fingerprinted"))
			}
			continue
		}
		if err := g.Begin(item); err != nil {
			return nil, nil, stats, err
		}
		if err := g.Complete(item); err != nil {
			return nil, nil, stats, err
		}
		if st := cp.Track(tt.TrackID); st != nil {
			st.AddStage(media.StageFingerprint)
		}
	}
	// Recompute duplicate flags and ordering over the complete set.
	fingerprint.RecomputeDuplicates(results)
	sort.Slice(results, func(i, j int) bool { return results[i].SegmentID < results[j].SegmentID })
	return results, allErrs, stats, nil
}

func mergeFingerprints(existing, added []fingerprint.Result) []fingerprint.Result {
	byID := map[string]fingerprint.Result{}
	for _, r := range existing {
		byID[r.SegmentID] = r
	}
	for _, r := range added {
		byID[r.SegmentID] = r
	}
	out := make([]fingerprint.Result, 0, len(byID))
	for _, r := range byID {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SegmentID < out[j].SegmentID })
	return out
}

// buildProvenance assembles the sealed provenance record. Wall-clock timing is
// deliberately excluded so identical runs hash identically.
func buildProvenance(m *Manifest, in *track.Input, opts Options, stats Stats) (*provenance.Record, error) {
	stages := []provenance.StageRecord{
		{Stage: media.StageParse, Version: track.ParseVersion},
		{Stage: media.StageTimeline, Version: track.ParseVersion},
		{Stage: media.StageSplit, Version: provenance.ProcessingVersion},
		{Stage: media.StageSilence, Version: provenance.ProcessingVersion},
		{Stage: media.StageFingerprint, Version: fingerprint.Algorithm},
	}
	var trackIDs, rejected, truncated []string
	for _, v := range m.Tracks {
		trackIDs = append(trackIDs, v.ID)
		if v.Rejected {
			rejected = append(rejected, v.ID)
		}
		if v.Truncated {
			truncated = append(truncated, v.ID)
		}
	}
	// Resumption statistics are deliberately absent from the sealed record:
	// a resumed run must hash identically to an uninterrupted one.
	_ = stats
	rec := provenance.New(m.Input, stages, trackIDs, rejected, truncated)
	if !m.Complete {
		rec.MarkIncomplete()
	}
	if _, err := rec.Seal(); err != nil {
		return nil, err
	}
	return rec, nil
}

// elapsedMillis converts a duration to whole milliseconds without losing the
// distinction between "fast" and "instant".
func elapsedMillis(d time.Duration) int64 { return d.Milliseconds() }
