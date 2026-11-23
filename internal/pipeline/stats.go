package pipeline

import (
	"mediapipe/internal/audio"
	"mediapipe/internal/fault"
	"mediapipe/internal/manifest"
	"mediapipe/internal/model"
	"mediapipe/internal/transform"
)

// measureBitrate derives the achieved video kbps from video payload bytes over
// the video timeline span, matching the bandwidth-policy target's media-only
// definition, then returns the absolute deviation percentage versus target.
func measureBitrate(tl *model.Timeline, targetKbps int) (float64, float64) {
	var bytes int64
	var vStart, vEnd int64
	haveVideo := false
	for _, tr := range tl.Tracks {
		if tr.Kind != model.KindVideo || len(tr.Samples) == 0 {
			continue
		}
		haveVideo = true
		first := tr.Samples[0].PTS + tr.OffsetNanos
		last := tr.Samples[len(tr.Samples)-1].PTS +
			tr.Samples[len(tr.Samples)-1].DurationNanos + tr.OffsetNanos
		if first < vStart || vEnd == 0 {
			vStart = first
		}
		if last > vEnd {
			vEnd = last
		}
		for _, s := range tr.Samples {
			if s.PayloadLen > 0 {
				bytes += s.PayloadLen
			}
		}
	}
	if !haveVideo {
		return 0, 0
	}
	spanNanos := vEnd - vStart
	if spanNanos <= 0 {
		return 0, 0
	}
	seconds := float64(spanNanos) / 1e9
	actualKbps := float64(bytes*8) / 1000.0 / seconds
	if targetKbps <= 0 {
		return actualKbps, 0
	}
	dev := actualKbps - float64(targetKbps)
	if dev < 0 {
		dev = -dev
	}
	return actualKbps, dev / float64(targetKbps) * 100.0
}

// finalize marks recoverable faults as recovered when recovery mode is on and
// assembles the terminal result.
func finalize(tl *model.Timeline, m manifest.Manifest, body []byte,
	stats manifest.Stats, errs *fault.Batch,
	normalized map[string]transform.Normalized,
	silenceRes map[string]audio.Result) (*Result, error) {
	res := &Result{
		Timeline: tl, Manifest: m, ManifestBytes: body, Stats: stats,
		Errors: errs, Normalized: normalized, Silence: silenceRes,
	}
	if len(errs.Fatal()) > 0 {
		return res, errs.Fatal()[0]
	}
	return res, nil
}

// MarkRecovered flags every recoverable fault as resynchronized past. It is
// called by the CLI only in --recover mode so the recovered-unit count is
// explicit rather than implied.
func MarkRecovered(errs *fault.Batch) int {
	n := 0
	for _, e := range errs.All() {
		if e.Recoverable {
			e.Recovered = true
			n++
		}
	}
	return n
}

// sourceCorruptionOffset returns the byte offset of the first container-level
// corruption fault (truncation or unknown structure). These are the faults the
// acceptance report means by source_error_offset; clock and packet faults have
// their own counters. Returns -1 when the input containers are intact.
func sourceCorruptionOffset(errs *fault.Batch) int64 {
	for _, e := range errs.All() {
		if e.Kind == fault.KindTruncated || e.Kind == fault.KindUnknown {
			return e.Offset
		}
	}
	return -1
}

// HasFatal reports whether the run ended on a non-recoverable invariant.
func HasFatal(errs *fault.Batch) bool { return len(errs.Fatal()) > 0 }

var _ = model.KindAudio
