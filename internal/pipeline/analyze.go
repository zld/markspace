package pipeline

import (
	"github.com/archive/mediakernel/internal/manifest"
	"github.com/archive/mediakernel/internal/media"
	"github.com/archive/mediakernel/internal/rtprecover"
	"github.com/archive/mediakernel/internal/timeline"
)

// analyze performs stages 5-7: cut segments on decodable boundaries, detect
// silence, fingerprint and dedup, then assemble the sorted result manifest.
// Confirmed gaps are never crossed: a segment that would span a gap is split at
// the gap boundary so no continuous content is fabricated.
func analyze(frames []recoveredFrame, gaps []manifest.LossGap, idx *media.Index,
	axis *timeline.Axis, in *Input, cfg Config, digest string, rebuilt bool,
	cp *manifest.Checkpoint) (*manifest.ResultManifest, *Stats, error) {

	audioTrack := idx.AudioTrack()
	var tr *media.TrackIndex
	if audioTrack >= 0 {
		tr = idx.Tracks[audioTrack]
	}

	// Resume: a checkpoint records the timeline point reached by an earlier
	// (interrupted) run. The full manifest is still re-derived for audit, but
	// segments already completed are counted as recovered and do not advance
	// the checkpoint again; only newly completed work extends it.
	resumedBoundary := int64(0)
	if cp != nil {
		resumedBoundary = cp.LastTimelineUS
	}

	// Build segments on the configured cadence, snapping requested cuts to the
	// nearest decodable keyframe and recording the boundary deviation.
	segDur := cfg.SegmentDurationUS
	presentEnd := int64(axis.TimelineDuration())
	var segments []manifest.Segment
	var segContents []media.SegmentContent
	var audioFrames []media.AudioFrame
	var maxDev int64

	cut := int64(0)
	segIndex := 0
	cursor := 0
	for cut < presentEnd {
		requestedEnd := cut + segDur
		if requestedEnd > presentEnd {
			requestedEnd = presentEnd
		}
		actualEnd := requestedEnd
		var dev int64
		if tr != nil {
			boundary, d, err := media.NearestKeyframe(tr, requestedEnd)
			if err != nil {
				return nil, nil, err
			}
			actualEnd, dev = boundary, d
		}
		if d := abs64(dev); d > maxDev {
			maxDev = d
		}
		// A keyframe fallback must never move the boundary to or before the
		// current cut; clamp forward so the loop always makes progress.
		if actualEnd <= cut {
			actualEnd = requestedEnd
		}

		// Collect frames inside [cut, actualEnd), stopping at any gap so the
		// segment never straddles unknown content.
		var pcm []byte
		var rawBytes int64
		segStart := cut
		hitGap := false
		for cursor < len(frames) && frames[cursor].startUS < actualEnd {
			f := frames[cursor]
			if rtprecover.Covers(gaps, f.startUS, f.endUS) {
				// Close the current segment before the gap; the next segment
				// begins at this frame's position, after the unknown region.
				cut = f.startUS
				cursor++
				hitGap = true
				break
			}
			pcm = append(pcm, f.pcm...)
			rawBytes += int64(f.rawBytes)
			audioFrames = append(audioFrames, media.AudioFrame{
				StartUS: f.startUS, EndUS: f.endUS, PCM: f.pcm,
			})
			cursor++
		}
		if !hitGap {
			cut = actualEnd
		}
		if len(pcm) == 0 {
			continue
		}
		hash, err := media.Fingerprint(pcm, cfg.Fingerprint)
		if err != nil {
			return nil, nil, err
		}
		seg := manifest.Segment{
			Index: segIndex, StartUS: segStart, EndUS: cut, KeyframeUS: actualEnd,
			BoundaryDevUS: dev, Bytes: rawBytes, Fingerprint: hash, SourceAsset: in.Asset.ID,
		}
		segments = append(segments, seg)
		segContents = append(segContents, media.SegmentContent{
			ID: in.Asset.ID, StartUS: segStart, EndUS: cut, PCM: pcm,
		})
		segIndex++

		// Only work beyond the resumed boundary is newly completed and extends
		// the checkpoint; recovered segments are never re-checkpointed.
		if cut > resumedBoundary {
			_ = saveCheckpoint(cfg.WorkDir, &manifest.Checkpoint{
				CompletedSegments: segIndex,
				LastTimelineUS:    cut,
			})
		}
	}

	// Silence on the same axis; unknown intervals carry explicit statuses.
	silences, err := media.DetectSilence(audioFrames, gaps, cfg.Silence)
	if err != nil {
		return nil, nil, err
	}

	// Fingerprints with provenance; gap-straddling duplicates are not merged.
	fps, err := media.FingerprintAndDedup(segContents, gaps, cfg.Fingerprint)
	if err != nil {
		return nil, nil, err
	}

	result := &manifest.ResultManifest{
		SchemaVersion: 1,
		InputDigest:   digest,
		IndexRebuilt:  rebuilt,
		Segments:      segments,
		Silences:      silences,
		Fingerprints:  fps,
		Gaps:          gaps,
	}
	stats := buildStats(result, in, cfg, maxDev, resumedBoundary)
	return result, stats, nil
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
