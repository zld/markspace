package rtpreplay

import (
	"fmt"
	"sort"
)

// Metric units are part of the report contract. Every reported number names
// its unit so terminal output and JSON cannot be compared across mismatched
// scales.
const (
	UnitMilliseconds = "ms"
	UnitTicks        = "ticks"
	UnitPackets      = "packets"
	UnitBitsPerSec   = "bps"
	UnitPercent      = "pct"
	UnitScore        = "score_0_100"
)

// LatencySummary reports one-way delay between the trace arrival clock and the
// reconstructed media clock, in milliseconds.
type LatencySummary struct {
	Count  int     `json:"count"`
	P50Ms  float64 `json:"p50_ms"`
	P90Ms  float64 `json:"p90_ms"`
	P99Ms  float64 `json:"p99_ms"`
	MaxMs  float64 `json:"max_ms"`
	MinMs  float64 `json:"min_ms"`
	Unit   string  `json:"unit"`
	Source string  `json:"source"`
}

// BitrateInterval is one bandwidth-change interval mapped onto media time.
type BitrateInterval struct {
	StartMs               float64 `json:"start_ms"`
	EndMs                 float64 `json:"end_ms"`
	TargetBps             int64   `json:"target_bps"`
	ActualBps             int64   `json:"actual_bps"`
	DeviationPct          float64 `json:"deviation_pct"`
	PayloadBytes          int64   `json:"payload_bytes"`
	MediaDurationMs       float64 `json:"media_duration_ms"`
	LostPacketsInInterval uint64  `json:"lost_packets_in_interval"`
}

// QualityInput names the objective method and its applicability limits. The
// score is a transport-derived estimate only; it is not a perceptual MOS.
type QualityInput struct {
	Method      string `json:"method"`
	Limitations string `json:"limitations"`
	Unit        string `json:"unit"`
}

// Metrics is the regression subsystem's output for one stream.
type Metrics struct {
	Track               string            `json:"track"`
	Codec               Codec             `json:"codec"`
	ClockHz             uint32            `json:"clock_hz"`
	Timebase            string            `json:"timebase"`
	LossPackets         uint64            `json:"loss_packets"`
	DuplicatePackets    int               `json:"duplicate_packets"`
	ReorderedPackets    int               `json:"reordered_packets"`
	LatePackets         int               `json:"late_packets"`
	ReceivedPackets     int               `json:"received_packets"`
	DeliveredPackets    int               `json:"delivered_packets"`
	BurstLossRuns       []GapRun          `json:"burst_loss_runs"`
	BurstLossPackets    uint64            `json:"burst_loss_packets"`
	LossPct             float64           `json:"loss_pct"`
	Latency             LatencySummary    `json:"latency"`
	Bitrate             BitrateSummary    `json:"bitrate"`
	Intervals           []BitrateInterval `json:"bitrate_intervals"`
	QualityScore        float64           `json:"quality_score"`
	QualityInput        QualityInput      `json:"quality_input"`
	MediaDurationMs     float64           `json:"media_duration_ms"`
	MeasurementWindowMs float64           `json:"measurement_window_ms"`
}

// BitrateSummary is the whole-stream bitrate comparison.
type BitrateSummary struct {
	TargetBps    int64   `json:"target_bps"`
	ActualBps    int64   `json:"actual_bps"`
	DeviationPct float64 `json:"deviation_pct"`
	Unit         string  `json:"unit"`
}

// ComputeMetrics derives transport and bitrate metrics from reconstructed
// packet states and bandwidth events. All aggregation uses integer byte and
// tick counts; float conversion happens only at the report boundary.
func ComputeMetrics(ss *StreamState, events []TimedEvent) (*Metrics, error) {
	m := &Metrics{
		Track: ss.Track, Codec: ss.Codec, ClockHz: ss.ClockHz,
		Timebase:         fmt.Sprintf("%d/1", ss.ClockHz),
		DuplicatePackets: ss.Stats.Duplicates,
		ReorderedPackets: ss.Stats.Reordered,
		LatePackets:      ss.Stats.Late,
		ReceivedPackets:  ss.Stats.Received,
		DeliveredPackets: ss.Stats.Delivered,
		LossPackets:      ss.Stats.GapPackets,
		BurstLossRuns:    append([]GapRun(nil), ss.Gaps...),
		BurstLossPackets: ss.Stats.GapPackets,
	}
	total := uint64(ss.Stats.Delivered) + ss.Stats.GapPackets
	if total > 0 {
		m.LossPct = float64(ss.Stats.GapPackets) * 100 / float64(total)
	}
	m.MediaDurationMs = MillisF(ss.MediaTicks, ss.ClockHz)
	m.Latency = latencySummary(ss)
	bit, err := computeBitrate(ss, events)
	if err != nil {
		return nil, err
	}
	m.Bitrate = bit.summary
	m.Intervals = bit.intervals
	m.MeasurementWindowMs = bit.windowMs
	m.QualityScore = qualityScore(ss, m)
	m.QualityInput = QualityInput{
		Method: "transport-weighted PLR + burst penalty (non-perceptual)",
		Limitations: "derived from sequence/timestamp evidence only; not a MOS, " +
			"does not model codec concealment, PLC tuning, or jitter-buffer strategy",
		Unit: UnitScore,
	}
	return m, nil
}

func latencySummary(ss *StreamState) LatencySummary {
	vals := make([]int64, 0, len(ss.States))
	for _, st := range ss.States {
		if st.Kind == "packet" {
			vals = append(vals, st.LatencyTicks)
		}
	}
	out := LatencySummary{Count: len(vals), Unit: UnitMilliseconds,
		Source: "arrival_ms minus reconstructed media_ms at the packet start; absolute sender-to-trace delay"}
	if len(vals) == 0 {
		return out
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	toMs := func(t int64) float64 { return float64(t) * 1000 / float64(ss.ClockHz) }
	out.MinMs = toMs(vals[0])
	out.MaxMs = toMs(vals[len(vals)-1])
	out.P50Ms = toMs(percentile(vals, 0.50))
	out.P90Ms = toMs(percentile(vals, 0.90))
	out.P99Ms = toMs(percentile(vals, 0.99))
	return out
}

// percentile uses the nearest-rank method on a sorted slice.
func percentile(sorted []int64, q float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(q*float64(len(sorted)) + 0.999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

type bitrateResult struct {
	summary   BitrateSummary
	intervals []BitrateInterval
	windowMs  float64
}

func computeBitrate(ss *StreamState, events []TimedEvent) (bitrateResult, error) {
	res := bitrateResult{summary: BitrateSummary{Unit: UnitBitsPerSec}}
	var bw []TimedEvent
	for _, ev := range events {
		if ev.Kind == EventBandwidth && (ev.Track == "" || ev.Track == ss.Track) {
			bw = append(bw, ev)
		}
	}
	if len(bw) == 0 {
		return res, NewError(CodeInvalidConfig, "bitrate", "no bandwidth events for stream").WithTrack(ss.Track)
	}
	sort.SliceStable(bw, func(i, j int) bool { return bw[i].AtMs < bw[j].AtMs })
	if bw[0].AtMs > 0 {
		// The first target applies from the media origin.
		bw = append([]TimedEvent{{AtMs: 0, Kind: EventBandwidth, ValueBps: bw[0].ValueBps, Note: "backfilled"}}, bw...)
	}
	for i := range bw {
		startTicks, err := DurationTicks(uint64(bw[i].AtMs), ss.ClockHz)
		if err != nil {
			return res, AsError(err)
		}
		endTicks := ss.MediaTicks
		if i+1 < len(bw) {
			endTicks, err = DurationTicks(uint64(bw[i+1].AtMs), ss.ClockHz)
			if err != nil {
				return res, AsError(err)
			}
		}
		if endTicks > ss.MediaTicks {
			endTicks = ss.MediaTicks
		}
		if startTicks > endTicks {
			continue
		}
		var bytes, lost uint64
		for _, st := range ss.States {
			if st.Kind == "gap" {
				if st.MediaStartTicks >= startTicks && st.MediaStartTicks < endTicks {
					lost += st.GapCount
				}
				continue
			}
			// Attribute each packet to the interval containing its start.
			if st.MediaStartTicks >= startTicks && st.MediaStartTicks < endTicks {
				bytes += uint64(st.SizeBytes)
			}
		}
		mediaMs := MillisF(endTicks-startTicks, ss.ClockHz)
		actual := int64(0)
		if mediaMs > 0 {
			actual = int64(float64(bytes) * 8 * 1000 / mediaMs)
		}
		dev := 0.0
		if bw[i].ValueBps > 0 {
			dev = (float64(actual) - float64(bw[i].ValueBps)) * 100 / float64(bw[i].ValueBps)
		}
		res.intervals = append(res.intervals, BitrateInterval{
			StartMs: MillisF(startTicks, ss.ClockHz), EndMs: MillisF(endTicks, ss.ClockHz),
			TargetBps: bw[i].ValueBps, ActualBps: actual, DeviationPct: dev,
			PayloadBytes: int64(bytes), MediaDurationMs: mediaMs,
			LostPacketsInInterval: lost,
		})
	}
	// Whole-stream numbers weighted by interval duration.
	var weightedTarget float64
	var coveredMs float64
	var allBytes int64
	for _, iv := range res.intervals {
		weightedTarget += float64(iv.TargetBps) * iv.MediaDurationMs
		coveredMs += iv.MediaDurationMs
		allBytes += iv.PayloadBytes
	}
	if coveredMs > 0 {
		res.summary.TargetBps = int64(weightedTarget / coveredMs)
	}
	if coveredMs > 0 {
		res.summary.ActualBps = int64(float64(allBytes) * 8 * 1000 / coveredMs)
	}
	if res.summary.TargetBps > 0 {
		res.summary.DeviationPct = (float64(res.summary.ActualBps) - float64(res.summary.TargetBps)) *
			100 / float64(res.summary.TargetBps)
	}
	res.windowMs = coveredMs
	return res, nil
}

// qualityScore is a transport-derived 0-100 estimate. It starts at 100 and
// deducts for packet loss (linear) and for burstiness (longest run), the two
// quantities the trace can actually evidence. It deliberately does not claim
// perceptual accuracy.
func qualityScore(ss *StreamState, m *Metrics) float64 {
	score := 100.0
	score -= 2.5 * m.LossPct
	longest := uint64(0)
	for _, run := range ss.Gaps {
		if run.Count > longest {
			longest = run.Count
		}
	}
	score -= float64(longest) * 1.5
	if score < 0 {
		score = 0
	}
	return score
}
