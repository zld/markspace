package pipeline

import (
	"rtx/internal/clock"
	"rtx/internal/model"
	"rtx/internal/quality"
	"rtx/internal/segment"
	"rtx/internal/transform"
)

// Options carries the operator-facing thresholds.
type Options struct {
	ParseOptions
	MinQuality      float64 // minimum acceptable quality score
	MaxBitrate      int64   // hard bitrate ceiling in bit/s
	TargetBPS       int64   // target bitrate in bit/s
	BudgetMS        int64   // processing budget
	MaxDriftPPM     float64 // drift threshold
	CostPerPacketUS float64 // deterministic cost model: RTP packet
	CostPerBlockUS  float64 // deterministic cost model: WAV sample block
}

// Analysis is the stage-2 output: timing, drift, segments, quality and
// the generated configuration.
type Analysis struct {
	SampleCounts []uint64          // per-packet cumulative sample counts
	Segments     []segment.Segment // variable-frame-rate intervals
	Drift        *clock.Result
	Quality      *quality.Metrics
	DurationNS   int64 // media duration spanned by the packet cadence
	BitrateBPS   int64
	CostMS       float64
	Config       *transform.Config
	Anomalies    []model.Anomaly
}

// Default cost model: one RTP packet costs 100 µs of processing, one
// WAV sample block 1 µs.
const (
	DefaultCostPerPacketUS = 100.0
	DefaultCostPerBlockUS  = 1.0
)

// Analyze runs stage 2 over the parsed input. Media cadence (one frame
// per packet, segment frame sizes) forms the monotonic sample time
// base; unwrapped RTP ticks are regressed against it for drift.
func Analyze(in *Input, opts Options) *Analysis {
	a := &Analysis{}
	pts := in.Unwrapped.Points
	clockRate := uint64(in.Timescale)
	sampleRate := uint64(in.WAV.Header.SampleRate)
	// Per-packet sample counts from unwrapped RTP ticks.
	if len(pts) > 0 {
		base := pts[0].Unwrapped
		a.SampleCounts = make([]uint64, len(pts))
		for i, p := range pts {
			a.SampleCounts[i] = (p.Unwrapped - base) * sampleRate / clockRate
		}
	}
	a.Segments = segment.Build(a.SampleCounts, segment.MinPackets)
	// Monotonic media time: each sequence step advances one frame of
	// the segment it falls in. Isolated or late packets leave holes in
	// the point list; the sequence difference spans them so the media
	// clock stays honest instead of fabricating samples.
	var driftSamples []clock.Sample
	var cum uint64
	segOf := func(pkt int) uint64 {
		for _, s := range a.Segments {
			if pkt >= s.StartPacket && pkt < s.EndPacket && s.FrameSamples > 0 {
				return s.FrameSamples
			}
		}
		if len(a.Segments) > 0 {
			return a.Segments[0].FrameSamples
		}
		return 1
	}
	for i, p := range pts {
		if i > 0 {
			gap := int(uint16(p.Packet.Seq - pts[i-1].Packet.Seq))
			if gap < 1 {
				gap = 1
			}
			cum += uint64(gap) * segOf(i-1)
		}
		driftSamples = append(driftSamples, clock.Sample{
			Index:    i,
			SampleNS: model.SamplesToNS(cum, sampleRate),
			RTPNS:    model.RTPToNS(p.Unwrapped-pts[0].Unwrapped, clockRate),
		})
	}
	a.Drift = clock.Fit(driftSamples, opts.MaxDriftPPM)
	a.Anomalies = append(a.Anomalies, a.Drift.Anomalies...)
	// Quality over the retained WAV blocks only. The expectation is in
	// per-channel samples: declared frames times channel count.
	expected := int64(in.WAV.Header.FrameCount()) * int64(in.WAV.Header.Channels)
	a.Quality = quality.Measure(in.WAV.Blocks, in.WAV.Header.BitsPerSample, expected)
	// Bitrate: payload bits over the media duration spanned by packets.
	var payloadBytes int64
	for _, p := range in.RTP.Packets {
		payloadBytes += int64(p.Payload)
	}
	var durationNS int64
	if len(driftSamples) > 1 {
		durationNS = driftSamples[len(driftSamples)-1].SampleNS
	}
	a.DurationNS = durationNS
	if durationNS > 0 {
		a.BitrateBPS = payloadBytes * 8 * 1_000_000_000 / durationNS
	}
	pktCost, blkCost := opts.CostPerPacketUS, opts.CostPerBlockUS
	if pktCost <= 0 {
		pktCost = DefaultCostPerPacketUS
	}
	if blkCost <= 0 {
		blkCost = DefaultCostPerBlockUS
	}
	a.CostMS = (pktCost*float64(len(in.RTP.Packets)) +
		blkCost*float64(len(in.WAV.Blocks))) / 1000
	a.Config = transform.Generate(transform.Input{
		MinQuality:          opts.MinQuality,
		MeasuredQuality:     a.Quality.Score,
		TargetBitrateBPS:    opts.TargetBPS,
		EstimatedBitrateBPS: a.BitrateBPS,
		MaxBitrateBPS:       opts.MaxBitrate,
		BudgetMS:            opts.BudgetMS,
		EstimatedCostMS:     a.CostMS,
		DriftPPM:            a.Drift.PPM,
		MaxDriftPPM:         opts.MaxDriftPPM,
	})
	a.Anomalies = append(a.Anomalies, a.Config.Anomalies...)
	return a
}
