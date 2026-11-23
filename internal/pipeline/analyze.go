package pipeline

import (
	"mediapipe/internal/audio"
	"mediapipe/internal/fault"
	"mediapipe/internal/fingerprint"
	"mediapipe/internal/manifest"
	"mediapipe/internal/model"
	"mediapipe/internal/segment"
	"mediapipe/internal/testdata"
	"mediapipe/internal/clock"
)

// runSilence analyzes every audio track and returns results keyed by track ID.
func runSilence(tl *model.Timeline) map[string]audio.Result {
	cfg := audio.DefaultConfig()
	out := map[string]audio.Result{}
	for _, tr := range tl.Tracks {
		if tr.Kind == model.KindAudio {
			out[tr.ID] = audio.Analyze(tr, cfg)
		}
	}
	return out
}

// cutAndHash cuts each video track and hashes its segments. Audio hashing
// uses the first available audio track so content dedup covers both kinds.
func cutAndHash(tl *model.Timeline, silenceRes map[string]audio.Result,
	errs *fault.Batch) (map[string][]segment.Segment,
	map[string][]fingerprint.SegmentHash) {
	cutCfg := segment.DefaultConfig()
	hasher := fingerprint.DefaultHasher()
	segs := map[string][]segment.Segment{}
	hashes := map[string][]fingerprint.SegmentHash{}

	var audioTrack *model.Track
	var videoTrack *model.Track
	for _, tr := range tl.Tracks {
		switch tr.Kind {
		case model.KindAudio:
			if audioTrack == nil {
				audioTrack = tr
			}
		case model.KindVideo:
			if videoTrack == nil {
				videoTrack = tr
			}
		}
	}
	if videoTrack == nil {
		return segs, hashes
	}
	cutter, ferr := segment.New(videoTrack, cutCfg)
	if ferr != nil {
		errs.Add(ferr)
		return segs, hashes
	}
	var silences []audio.Interval
	for _, r := range silenceRes {
		silences = append(silences, r.Intervals...)
	}
	cut := cutter.Cut(silences, errs)
	segs[videoTrack.ID] = cut
	for _, sg := range cut {
		hashes[videoTrack.ID] = append(hashes[videoTrack.ID],
			hasher.HashSegment(sg.Index, videoTrack, audioTrack,
				sg.StartSample, sg.EndSample))
	}
	return segs, hashes
}

// flattenHashes orders per-track segment hashes into one deterministic slice.
func flattenHashes(
	hashes map[string][]fingerprint.SegmentHash) []fingerprint.SegmentHash {
	trackIDs := make([]string, 0, len(hashes))
	for id := range hashes {
		trackIDs = append(trackIDs, id)
	}
	// Simple insertion sort keeps ordering deterministic without importing
	// sort into this stage's hot path.
	for i := 1; i < len(trackIDs); i++ {
		for j := i; j > 0 && trackIDs[j-1] > trackIDs[j]; j-- {
			trackIDs[j-1], trackIDs[j] = trackIDs[j], trackIDs[j-1]
		}
	}
	var out []fingerprint.SegmentHash
	for _, id := range trackIDs {
		out = append(out, hashes[id]...)
	}
	return out
}

// toSegmentRecords converts internal cuts into manifest segment records.
func toSegmentRecords(segs map[string][]segment.Segment,
	hashes map[string][]fingerprint.SegmentHash) []manifest.SegmentRecord {
	var out []manifest.SegmentRecord
	for trackID, list := range segs {
		hashByIndex := map[int]fingerprint.SegmentHash{}
		for _, h := range hashes[trackID] {
			hashByIndex[h.SegmentIndex] = h
		}
		for _, sg := range list {
			rec := manifest.SegmentRecord{
				Index: sg.Index, TrackID: trackID,
				StartNanos: sg.StartNanos, EndNanos: sg.EndNanos,
				DurationNanos: sg.EndNanos - sg.StartNanos,
				StartSample: sg.StartSample, EndSample: sg.EndSample,
				KeyframeCut: sg.KeyframeCut, SilenceCut: sg.SilenceCut,
			}
			if h, ok := hashByIndex[sg.Index]; ok {
				rec.VideoHash = h.VideoHash
				rec.AudioHash = h.AudioHash
			}
			out = append(out, rec)
		}
	}
	return out
}

// buildStats computes the terminal report statistics.
func buildStats(tl *model.Timeline, align clock.Result,
	silenceRes map[string]audio.Result,
	segs map[string][]segment.Segment,
	hashes map[string][]fingerprint.SegmentHash,
	matches []fingerprint.Match, opts Options,
	errs *fault.Batch) manifest.Stats {
	st := manifest.Stats{
		TargetKbps: opts.TargetKbps,
		WrapCount:  tl.WrapCount,
		QuarantinedPackets: tl.Quarantined,
		SourceErrorOffset: sourceCorruptionOffset(errs),
		RecoveredUnits: errs.RecoveredCount(),
		DuplicateMatches: len(matches),
		ManifestAlignmentMs: float64(align.MaxSkewNanos) / 1e6,
	}
	for _, list := range segs {
		st.Segments += len(list)
	}
	var totalSilence int64
	var rmsSum float64
	var rmsN int
	for _, r := range silenceRes {
		totalSilence += r.TotalNanos
		if r.Windows > 0 {
			rmsSum += r.RMS
			rmsN++
		}
	}
	st.SilenceMs = totalSilence / 1e6
	if rmsN > 0 {
		st.AudioRMS = rmsSum / float64(rmsN)
	}
	st.ActualKbps, st.BitrateDeviationPct = measureBitrate(tl, opts.TargetKbps)
	return st
}

var _ = testdata.CorruptDrift
