// Package app orchestrates the full investigation pipeline: RTP sequence
// recovery and codec duration parsing on one path, MP4 structure and
// keyframe scanning on the other, both feeding a bounded worker pool, a
// deterministic merge and a budget-checked report.
package app

import (
	"bytes"
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"tracecheck/internal/codec"
	"tracecheck/internal/fixture"
	"tracecheck/internal/merge"
	"tracecheck/internal/model"
	"tracecheck/internal/mp4"
	"tracecheck/internal/report"
	"tracecheck/internal/rtp"
	"tracecheck/internal/worker"
)

// Config is the validated run configuration coming from the CLI.
type Config struct {
	Profile        string
	Seed           int64
	Workers        int
	Queue          int
	LossBudget     int
	KeyframeBudget model.Tick
	CancelAfter    int
	WindowTicks    model.Tick
}

// Outcome is everything the CLI needs to print and choose an exit code.
type Outcome struct {
	Report    *report.Report
	Stats     worker.Stats
	ExitCode  int
	Timeline  []model.TimelineEvent
}

// Analyze generates the requested trace and runs both analysis paths.
func Analyze(ctx context.Context, cfg Config) (*Outcome, error) {
	// The cancel profile owns its internal deadline: after CancelAfter
	// jobs complete, the pipeline cancels itself and drains the pool.
	var cancel context.CancelFunc
	if cfg.Profile == fixture.ProfileCancel && cfg.CancelAfter > 0 {
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
	}

	trace, err := fixture.Generate(fixture.Options{
		Profile:     cfg.Profile,
		Seed:        cfg.Seed,
		CancelAfter: cfg.CancelAfter,
	})
	if err != nil {
		return nil, err
	}

	builder := report.NewBuilder(cfg.Profile, cfg.Seed, report.Budget{
		MaxLossPackets:      cfg.LossBudget,
		MaxKeyframeInterval: cfg.KeyframeBudget,
	})

	// RTP path: per-stream sequence recovery then codec duration parsing.
	packetEvents, packetCount, duplicates := analyzeRTP(ctx, cfg, trace, builder)

	// MP4 path: scan structure and keyframes, record keyframe budget gaps.
	trackCount, keyframeCount, mp4Events := analyzeMP4(cfg, trace, builder)

	// Throughput path: fixed windows plus flow-change anomalies, kept
	// strictly separate from media packet durations.
	flowEvents, flowAnomalies := analyzeFlow(cfg, trace)
	builder.AddAnomalies(flowAnomalies)
	builder.SetFlowWindowCount(len(merge.Throughput(trace.FlowSamples, cfg.WindowTicks)))

	// Deterministic merge of every event on the unified timeline.
	timeline := merge.Merge(packetEvents, mp4Events, flowEvents)
	builder.SetTimeline(timeline)

	// Run the assembled jobs through the bounded pool, honoring cancel.
	pool := worker.New(ctx, cfg.Workers, cfg.Queue)
	results, stats := runJobs(cfg, pool, trace, cancel)
	_ = results

	canceled := stats.Canceled
	r := builder.Build(packetCount, trackCount, keyframeCount, duplicates, canceled)

	exit := 0
	switch {
	case canceled:
		exit = 130
	case !r.Summary.Passed:
		exit = 2
	}
	return &Outcome{Report: r, Stats: stats, ExitCode: exit, Timeline: timeline}, nil
}

func analyzeRTP(ctx context.Context, cfg Config, trace *fixture.Trace, b *report.Builder) (
	[]model.TimelineEvent, int, int,
) {
	// Each SSRC has its own independent 16-bit sequence space; buffers must
	// never be shared across streams or every alternating number looks lost.
	type streamBuf struct {
		id  uint32
		buf *rtp.ReorderBuffer
	}
	var streams []streamBuf
	byID := map[uint32]*rtp.ReorderBuffer{}
	for _, p := range trace.Packets {
		buf, ok := byID[p.Stream]
		if !ok {
			buf = rtp.NewReorderBuffer(8)
			byID[p.Stream] = buf
			streams = append(streams, streamBuf{id: p.Stream, buf: buf})
		}
		buf.Push(p)
	}

	var events []model.TimelineEvent
	packetCount := 0
	duplicates := 0
	var lossAnomalies []merge.Anomaly
	for _, sb := range streams {
		stream, buf := sb.id, sb.buf
		released := buf.Flush()
		packetCount += len(released)
		duplicates += buf.Duplicates()

		// Gaps become located findings and loss anomalies; no audio is
		// ever synthesized for missing numbers. One finding per missing
		// sequence keeps the loss count equal to the number of lost packets.
		for _, gap := range buf.Gaps() {
			for missing := gap.FromExtended; missing <= gap.ToExtended; missing++ {
				seq := missing
				f := model.NewFinding(model.ErrSequenceGap, model.SourceRTP, model.SeverityError,
					gap.Span, "missing packet %d on stream %d", missing, stream)
				f.Stream = stream
				f.Sequence = &seq
				b.AddFinding(f)
				b.AddConclusion(report.Conclusion{
					Item: fmt.Sprintf("gap:%d", missing),
					Sequence: missing, Span: gap.Span,
					Status: "missing", Detail: "gap declared after reorder window",
					Codes: []model.ErrorCode{model.ErrSequenceGap},
				})
			}
			lossAnomalies = append(lossAnomalies, merge.Anomaly{
				Span: gap.Span, LossCount: int(gap.Count),
				Reasons: []string{"burst_loss"},
			})
			events = append(events, model.TimelineEvent{
				Kind: model.EventGap, Start: gap.Span.Start, End: gap.Span.End,
				Stream: stream, Sequence: gap.FromExtended,
			})
		}

		// Codec duration parsing for every recovered packet; cumulative
		// durations build a monotonic per-stream timeline.
		var cursor model.Tick
		var maxRTP uint32
		haveRTP := false
		for _, p := range released {
			start, end, codes, status := parsePacket(p, &cursor)
			// RTP timestamps must advance with the sequence; any value
			// below the highest already seen is clock damage distinct
			// from packet reordering, even if a later packet recovers.
			if haveRTP && p.RTPTimestamp < maxRTP {
				seq := p.ExtendedSequence
				f := model.NewFinding(model.ErrTimestampRegress, model.SourceRTP,
					model.SeverityWarning, model.NewSpan(start, end),
					"rtp timestamp %d below stream max %d on stream %d",
					p.RTPTimestamp, maxRTP, stream)
				f.Stream = stream
				f.Sequence = &seq
				b.AddFinding(f)
				codes = append(codes, model.ErrTimestampRegress)
			}
			if !haveRTP || p.RTPTimestamp > maxRTP {
				maxRTP = p.RTPTimestamp
			}
			haveRTP = true
			verdict := report.Conclusion{
				Item: fmt.Sprintf("packet:%d", p.ExtendedSequence),
				Sequence: p.ExtendedSequence, Span: model.NewSpan(start, end),
				Status: status, Codes: codes,
			}
			b.AddConclusion(verdict)
			for _, code := range codes {
				if code == model.ErrTimestampRegress {
					continue // already emitted above with full context
				}
				f := model.NewFinding(code, model.SourceCodec, severityFor(code),
					model.NewSpan(start, end), "packet %d: %s", p.ExtendedSequence, status)
				f.Stream = stream
				seq := p.ExtendedSequence
				f.Sequence = &seq
				b.AddFinding(f)
			}
			events = append(events, model.TimelineEvent{
				Kind: model.EventPacket, Start: start, End: end,
				Stream: stream, Sequence: p.ExtendedSequence,
			})
		}
	}
	// Merge once across streams so adjacent loss runs carry their full
	// count into one budget-checked interval.
	b.AddAnomalies(merge.MergeAnomalies(lossAnomalies))
	return events, packetCount, duplicates
}

func parsePacket(p model.PacketRecord, cursor *model.Tick) (start, end model.Tick, codes []model.ErrorCode, status string) {
	start = *cursor
	var duration model.Tick
	switch p.Codec {
	case model.CodecOpus:
		res, err := codec.ParseOpus(p.Payload)
		if err != nil {
			codes = append(codes, classifyCodecError(err))
			status = "duration_undecidable"
			end = start
			return
		}
		duration, _ = model.TicksFromSamples(res.Samples, 48_000)
		status = "accepted_opus"
	case model.CodecAAC:
		res, err := codec.ParseADTS(p.Payload)
		if err != nil {
			codes = append(codes, classifyCodecError(err))
			status = "duration_undecidable"
			end = start
			return
		}
		duration = res.Duration
		status = "accepted_aac"
	default:
		codes = append(codes, model.ErrUnsupportedCodec)
		status = "unsupported_codec"
		return
	}
	end = start + duration
	*cursor = end
	return
}

func classifyCodecError(err error) model.ErrorCode {
	if err == codec.ErrTruncated {
		return model.ErrTruncatedPacket
	}
	return model.ErrInvalidDuration
}

func severityFor(code model.ErrorCode) model.Severity {
	if code == model.ErrTruncatedPacket {
		return model.SeverityError
	}
	return model.SeverityWarning
}

func analyzeMP4(cfg Config, trace *fixture.Trace, b *report.Builder) (int, int, []model.TimelineEvent) {
	res, err := mp4.Scan(bytes.NewReader(trace.MP4))
	if err != nil {
		f := model.NewFinding(model.ErrInputRejected, model.SourceMP4, model.SeverityError,
			model.Span{}, "mp4 scan failed: %v", err)
		b.AddFinding(f)
		return 0, 0, nil
	}
	var events []model.TimelineEvent
	keyframeCount := 0
	for _, tr := range res.Tracks {
		b.AddFindings(tr.Findings)
		for _, f := range tr.Findings {
			b.AddConclusion(report.Conclusion{
				Item: fmt.Sprintf("track:%d", tr.TrackID), Track: tr.TrackID,
				Span: f.Span, Status: string(f.Code), Codes: []model.ErrorCode{f.Code},
			})
		}
		for _, kf := range tr.Keyframes {
			keyframeCount++
			events = append(events, model.TimelineEvent{
				Kind: model.EventKeyframe, Start: kf.At, End: kf.At, Track: tr.TrackID,
				Sequence: kf.Sample,
			})
		}
		// Keyframe budget: an interval above the configured limit fails.
		for i := 1; i < len(tr.Keyframes); i++ {
			interval := tr.Keyframes[i].At - tr.Keyframes[i-1].At
			if interval > cfg.KeyframeBudget {
				span := model.NewSpan(tr.Keyframes[i-1].At, tr.Keyframes[i].At)
				f := model.NewFinding(model.ErrKeyframeGap, model.SourceMP4, model.SeverityError,
					span, "keyframe interval %v exceeds budget %v on track %d",
					interval, cfg.KeyframeBudget, tr.TrackID)
				f.Track = tr.TrackID
				b.AddFinding(f)
			}
		}
	}
	return len(res.Tracks), keyframeCount, events
}

func analyzeFlow(cfg Config, trace *fixture.Trace) ([]model.TimelineEvent, []merge.Anomaly) {
	windows := merge.Throughput(trace.FlowSamples, cfg.WindowTicks)
	var events []model.TimelineEvent
	for _, w := range windows {
		events = append(events, model.TimelineEvent{
			Kind: model.EventFlow, Start: w.Span.Start, End: w.Span.End,
			Label: fmt.Sprintf("%dB/s", w.BytesPerSec),
		})
	}
	anomalies := merge.FlowChange(windows, 0.3)
	return events, merge.MergeAnomalies(anomalies)
}

func runJobs(cfg Config, p *worker.Pool, trace *fixture.Trace, cancel context.CancelFunc) ([]worker.Result, worker.Stats) {
	total := len(trace.Packets) + 1 // packets plus the MP4 scan job
	if cfg.Profile == fixture.ProfileCancel && cfg.CancelAfter > 0 {
		total = trace.PlannedJobs
	}
	var done atomicCounter
	for i := 0; i < total; i++ {
		id := i
		ok := p.Submit(worker.Job{
			ID:    id,
			Label: fmt.Sprintf("analysis-%d", id),
			Run: func(ctx context.Context) error {
				// Simulate work so the bounded pool actually schedules jobs.
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Millisecond):
				}
				if cancel != nil && done.inc() == cfg.CancelAfter {
					cancel()
				}
				return nil
			},
		})
		if !ok {
			break // producer stops accepting after cancellation
		}
	}
	return p.Close()
}

// atomicCounter is a tiny goroutine-safe completion counter.
type atomicCounter struct{ n int64 }

func (c *atomicCounter) inc() int {
	return int(atomic.AddInt64(&c.n, 1))
}
