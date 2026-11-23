package pipeline

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"mediamux/analyze"
)

// finalise computes quality, the result digest and checkpoint state.
func (r *Runner) finalise(res *Result, cp *Checkpoint, in *Input) {
	samples := make([]analyze.QualitySample, 0, len(res.Segments))
	var gaps []int64
	var rtpAgg *analyze.RTPResult
	ordered := append([]SegmentOutcome(nil), res.Segments...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Index < ordered[j].Index })
	for _, s := range ordered {
		samples = append(samples, s.Quality)
		gaps = append(gaps, s.KeyframeGaps...)
		if s.RTP != nil {
			if rtpAgg == nil {
				rtpAgg = &analyze.RTPResult{}
			}
			rtpAgg.Duplicates += s.RTP.Duplicates
			rtpAgg.Missing += s.RTP.Missing
		}
	}
	var rtpMissingMS int64
	for _, s := range res.Segments {
		if s.RTP != nil {
			rtpMissingMS += s.RTP.MissingMS
		}
	}
	// The unified manifest timeline is compared against the committed
	// segments only, so an interrupted run does not report drift for media it
	// never read.
	doc := MaskSegments(in.Manifest, 0, cp.Cursor)
	refs := RefOf(doc)
	durations := MediaDurationsMS(res.Segments, len(refs))
	alignment := CompareManifestToMedia(refs, durations, 2)
	res.Alignment = alignment
	res.Quality = analyze.ComputeQuality(samples, gaps, rtpAgg, analyze.QualityOptions{
		Window: r.opts.Window, TargetBPS: in.Manifest.TargetBPS,
		AlignmentMS:    in.Manifest.Alignment.ErrorMS(),
		AlignmentMaxMS: in.Manifest.Alignment.MaxMS(),
	})
	res.Quality.RTPMissingMS = rtpMissingMS
	res.PeakBufferBytes = r.designPeak(in)
	res.Cache = r.cache.Stats()
	res.CorruptSegments = 0
	for _, s := range res.Segments {
		if s.Corrupt {
			res.CorruptSegments++
		}
	}
	cp.ResultDigest = resultDigest(res)
	if err := saveCheckpoint(r.checkpointPath(), cp); err == nil {
		res.Checkpoint = cp
	}
	res.ResultSHA256 = cp.ResultDigest
}

func (r *Runner) muAppendEvents(res *Result, events []Event) {
	res.Events = append(res.Events, events...)
}

func (r *Runner) checkpointPath() string {
	if r.opts.Dir == "" {
		return ""
	}
	return r.opts.CheckpointPath
}

func (r *Runner) saveCheckpoint(cp *Checkpoint) error {
	path := r.checkpointPath()
	if path == "" {
		return nil
	}
	return cp.Save(path)
}

func saveCheckpoint(path string, cp *Checkpoint) error {
	if path == "" {
		return nil
	}
	return cp.Save(path)
}

// resultDigest hashes the ordered segment digests, their corruption verdicts
// and the exact quality summary into one stable fingerprint. A resumed run
// rehydrates outcomes for already-committed segments, so it produces the same
// digest as a cold run over identical input.
func resultDigest(res *Result) string {
	type fingerprint struct {
		Manifest string
		Segments []string
		Quality  analyze.QualitySummary
		Corrupt  int
	}
	fp := fingerprint{Quality: res.Quality, Corrupt: res.CorruptSegments}
	if res.Checkpoint != nil && res.Checkpoint.ManifestDigest != "" {
		fp.Manifest = res.Checkpoint.ManifestDigest
	}
	segs := append([]SegmentOutcome(nil), res.Segments...)
	sort.Slice(segs, func(i, j int) bool { return segs[i].Index < segs[j].Index })
	for _, s := range segs {
		fp.Segments = append(fp.Segments, fmt.Sprintf("%d:%s:%t", s.Index, s.Digest, s.Corrupt))
	}
	b, _ := json.Marshal(fp)
	return analyze.Digest(b)
}

func durationMS(ms int64) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}
