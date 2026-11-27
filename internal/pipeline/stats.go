package pipeline

import (
	"fmt"
	"math"
	"sort"

	"medtrace/internal/mederr"
	"medtrace/internal/state"
	"medtrace/internal/timeline"
)

// computeSync pairs each video block with the temporally nearest audio block
// on the shared nanosecond clock. Missing-side samples are counted, never
// faked as zero error. P95 latency is the 95th percentile block arrival
// spacing on the media timeline (non-negative by construction).
func computeSync(extract *MediaExtract) map[string]any {
	var video, audio []MediaBlock
	for _, b := range extract.Blocks {
		switch b.TrackID {
		case 1:
			video = append(video, b)
		case 2:
			audio = append(audio, b)
		}
	}
	// nearestBlock relies on ascending time order.
	sort.Slice(video, func(i, j int) bool { return video[i].TimeNs < video[j].TimeNs })
	sort.Slice(audio, func(i, j int) bool { return audio[i].TimeNs < audio[j].TimeNs })
	merged := timeline.MergeStable(toEvents(extract.Blocks))
	var diffsMs []float64
	var missingV, missingA int64
	for _, v := range video {
		nearest, ok := nearestBlock(audio, v.TimeNs)
		if !ok {
			missingA++
			continue
		}
		d := timeline.AbsDiff(v.TimeNs, nearest.TimeNs)
		diffsMs = append(diffsMs, float64(d)/1e6)
	}
	for _, a := range audio {
		if _, ok := nearestBlock(video, a.TimeNs); !ok {
			missingV++
		}
	}
	return map[string]any{
		"av_sync_error_ms":      mean(diffsMs),
		"max_sync_error_ms":     maxF(diffsMs),
		"sync_pairs":            len(diffsMs),
		"missing_video_samples": missingV,
		"missing_audio_samples": missingA,
		"p95_latency_ms":        percentile(interArrivalMs(merged), 95),
	}
}

func toEvents(blocks []MediaBlock) []timeline.Event {
	out := make([]timeline.Event, len(blocks))
	for i, b := range blocks {
		out[i] = timeline.Event{TimeNs: b.TimeNs, TrackID: b.TrackID, BlockSeq: b.BlockSeq}
	}
	return out
}

func nearestBlock(blocks []MediaBlock, target timeline.Timestamp) (MediaBlock, bool) {
	if len(blocks) == 0 {
		return MediaBlock{}, false
	}
	idx := sort.Search(len(blocks), func(i int) bool { return blocks[i].TimeNs >= target })
	if idx == 0 {
		return blocks[0], true
	}
	if idx == len(blocks) {
		return blocks[idx-1], true
	}
	prev, next := blocks[idx-1], blocks[idx]
	if timeline.AbsDiff(target, prev.TimeNs) <= timeline.AbsDiff(next.TimeNs, target) {
		return prev, true
	}
	return next, true
}

func interArrivalMs(merged []timeline.Event) []float64 {
	if len(merged) < 2 {
		return []float64{0}
	}
	out := make([]float64, 0, len(merged)-1)
	for i := 1; i < len(merged); i++ {
		d := int64(merged[i].TimeNs - merged[i-1].TimeNs)
		if d < 0 {
			d = 0
		}
		out = append(out, float64(d)/1e6)
	}
	return out
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func maxF(xs []float64) float64 {
	m := 0.0
	for _, x := range xs {
		if x > m || math.IsNaN(m) {
			m = x
		}
	}
	return m
}

// percentile returns the nearest-rank p-th percentile (0..100).
func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := append([]float64(nil), xs...)
	sort.Float64s(cp)
	rank := int(math.Ceil(p/100*float64(len(cp)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(cp) {
		rank = len(cp) - 1
	}
	return cp[rank]
}

func blockOffsets(extract *MediaExtract) []state.OffsetRange {
	if len(extract.Blocks) == 0 {
		return nil
	}
	min, max := extract.Blocks[0].HeaderStart, int64(0)
	for _, b := range extract.Blocks {
		if b.HeaderStart < min {
			min = b.HeaderStart
		}
		if b.BodyEnd > max {
			max = b.BodyEnd
		}
	}
	return []state.OffsetRange{{Input: "sample.webm", Start: min, End: max}}
}

func (r *Runner) assemble(run *state.Run, report *Report) (*Report, error) {
	report.CompletedStages = run.CompletedStages()
	for _, name := range report.CompletedStages {
		st, ok := run.Stage(name)
		if !ok {
			continue
		}
		switch name {
		case StageSync:
			report.AVSyncErrorMs = asFloat(st.Stats["av_sync_error_ms"])
			report.P95LatencyMs = asFloat(st.Stats["p95_latency_ms"])
			report.SyncPairs = asInt64(st.Stats["sync_pairs"])
			report.MissingVideo = asInt64(st.Stats["missing_video_samples"])
			report.MissingAudio = asInt64(st.Stats["missing_audio_samples"])
		case StageNetwork:
			fillNetwork(report, st.Stats["streams"])
		case StageGraph:
			report.Order = asUint64Slice(st.Stats["order"])
		case StageMedia:
			report.Offsets = st.Offsets
		}
	}
	if len(report.Offsets) == 0 {
		if st, ok := run.Stage(StageMedia); ok {
			report.Offsets = st.Offsets
		}
	}
	if math.IsNaN(report.P95LatencyMs) || report.P95LatencyMs < 0 {
		return nil, mederr.New(mederr.CatOverflow, "p95 latency must be a finite non-negative value")
	}
	return report, nil
}

func fillNetwork(report *Report, v any) {
	streams, ok := v.([]any)
	if !ok {
		// Fresh (not yet JSON-round-tripped) results use []map[string]any.
		if typed, ok2 := v.([]map[string]any); ok2 {
			streams = make([]any, len(typed))
			for i := range typed {
				streams[i] = typed[i]
			}
		} else {
			return
		}
	}
	var recovered, lost, dup, reo int64
	for _, item := range streams {
		s, ok := item.(map[string]any)
		if !ok {
			continue
		}
		recovered += asInt64(s["recovered"])
		lost += asInt64(s["lost"])
		dup += asInt64(s["duplicates"])
		reo += asInt64(s["reordered"])
	}
	report.RecoveredPackets = recovered
	report.LostPackets = lost
	report.DuplicatePackets = dup
	report.ReorderedPackets = reo
	if denom := recovered + lost; denom > 0 {
		report.LossRecoveryRate = float64(recovered) / float64(denom)
	}
}

func asFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int64:
		return float64(t)
	case int:
		return float64(t)
	default:
		return 0
	}
}

func asInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	default:
		return 0
	}
}

func asUint64Slice(v any) []uint64 {
	switch xs := v.(type) {
	case []uint64:
		return append([]uint64(nil), xs...)
	case []any: // JSON round-trip form
		out := make([]uint64, 0, len(xs))
		for _, x := range xs {
			out = append(out, uint64(asInt64(x)))
		}
		return out
	default:
		return nil
	}
}

// Validate checks acceptance thresholds and returns a descriptive error.
func (rep *Report) Validate(toleranceMs int64) error {
	if rep.AVSyncErrorMs > float64(toleranceMs) {
		return mederr.New(mederr.CatInvalidInput,
			"av_sync_error_ms %.3f exceeds tolerance %d ms", rep.AVSyncErrorMs, toleranceMs)
	}
	if rep.LossRecoveryRate < 0.8 || rep.LossRecoveryRate > 1.0 {
		return mederr.New(mederr.CatInvalidInput,
			"loss_recovery_rate %.3f outside [0.8,1.0]", rep.LossRecoveryRate)
	}
	if math.IsNaN(rep.P95LatencyMs) || rep.P95LatencyMs < 0 || math.IsInf(rep.P95LatencyMs, 0) {
		return mederr.New(mederr.CatOverflow, "p95 latency is not a finite non-negative value")
	}
	return nil
}

// String renders the human-readable summary.
func (rep *Report) String() string {
	return fmt.Sprintf(
		"av_sync_error_ms=%.3f\nloss_recovery_rate=%.3f\np95_latency_ms=%.3f\n"+
			"duplicate_packets=%d\nreordered_packets=%d\nlost_packets=%d\nrecovered_packets=%d\n"+
			"sync_pairs=%d missing_video=%d missing_audio=%d\ninput_sha256=%v\ncompleted_stages=%v\n"+
			"track_order=%v\nbyte_offsets=%v",
		rep.AVSyncErrorMs, rep.LossRecoveryRate, rep.P95LatencyMs,
		rep.DuplicatePackets, rep.ReorderedPackets, rep.LostPackets, rep.RecoveredPackets,
		rep.SyncPairs, rep.MissingVideo, rep.MissingAudio, rep.InputSHA256,
		rep.CompletedStages, rep.Order, rep.Offsets,
	)
}
