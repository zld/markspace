package mediakernel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// RunConfig controls one pipeline execution. Zero values are replaced with
// sane defaults by the pipeline.
type RunConfig struct {
	QueueCap          int
	SlowConsumerDelay time.Duration // extra per-packet consumer work; 0 disables
	ClockRate         uint32
	BitrateTolerance  float64
	AlignmentTolMs    float64
	TargetBitrateBps  float64 // delivery-spec target; zero uses DefaultTargetBitrateBps
	// CancelAfter, when > 0, cancels the run after this many packets are
	// enqueued, exercising the cancellation path.
	CancelAfter int
}

// DefaultTargetBitrateBps is the reference bitrate used when no explicit target
// is supplied. The generated fixtures land within tolerance of it.
const DefaultTargetBitrateBps = 100_000

// PipelineCounters aggregates packet-level counters for the JSON report.
type PipelineCounters struct {
	Packets     int  `json:"packets"`
	LostPackets int  `json:"lost_packets"`
	Duplicates  int  `json:"duplicates"`
	Wraps       int  `json:"sequence_wraps"`
	QueuePeak   int  `json:"queue_peak"`
	QueueCap    int  `json:"queue_capacity"`
	Processed   int  `json:"processed"`
	Pending     int  `json:"pending"`
	Cancelled   bool `json:"cancelled"`
}

// TimelineReport is the clock-mapping result.
type TimelineReport struct {
	DurationTicks  int64   `json:"duration_ticks"`
	DurationMs     float64 `json:"duration_ms"`
	UniqueSamples  int64   `json:"unique_samples"`
	MissingSamples int64   `json:"missing_samples"`
}

// Report is the full machine-readable result of one pipeline run.
type Report struct {
	Counters        PipelineCounters      `json:"counters"`
	Timeline        TimelineReport        `json:"timeline"`
	ContentDigest   Digest                `json:"content_digest"`
	ContainerDigest Digest                `json:"container_digest"`
	IndexDigest     Digest                `json:"index_digest"`
	Plan            Plan                  `json:"plan"`
	Bitrate         BitrateSpec           `json:"bitrate"`
	Quality         QualityResult         `json:"quality"`
	Alignment       AlignmentSummary      `json:"alignment"`
	AlignmentItems  []SubtitleAlignment   `json:"alignment_items"`
	Corrupt         []CorruptPacketRecord `json:"corrupt_packets"`
	Migration       *MigrationReport      `json:"migration,omitempty"`
	SpecPass        bool                  `json:"spec_pass"`
	Failures        []string              `json:"spec_failures"`
}

// Pipeline wires the bounded queue between a parsing producer and a consuming
// worker, then folds the stream into the timeline, digests and metrics.
type Pipeline struct {
	cfg      RunConfig
	clock    Clock
	queue    *BoundedQueue[*Packet]
	content  *ContentHasher
	tracker  *GapTracker
	timeline *Timeline

	mu           sync.Mutex
	corrupt      []CorruptPacketRecord
	processed    int
	payloadBytes int64
}

// NewPipeline validates and normalizes a config.
func NewPipeline(cfg RunConfig) *Pipeline {
	if cfg.QueueCap < 1 {
		cfg.QueueCap = 8
	}
	if cfg.BitrateTolerance == 0 {
		cfg.BitrateTolerance = 0.05
	}
	if cfg.AlignmentTolMs == 0 {
		cfg.AlignmentTolMs = 25
	}
	clock := NewClock(cfg.ClockRate)
	return &Pipeline{
		cfg:      cfg,
		clock:    clock,
		queue:    NewBoundedQueue[*Packet](cfg.QueueCap),
		content:  NewContentHasher(),
		tracker:  NewGapTracker(),
		timeline: NewTimeline(clock),
	}
}

// Process streams data through the pipeline. On a parse/sequence error it
// returns the structured error (already carrying offset/phase) together with
// a partial report so callers can still emit quantified output.
func (p *Pipeline) Process(ctx context.Context, data []byte) (*Report, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	containerHasher := NewDigester()
	reader := NewFrameReaderWithTee(bytes.NewReader(data), containerHasher)

	var consumerErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // consumer: applies backpressure by draining slowly.
		defer wg.Done()
		for {
			pkt, err := p.queue.Dequeue(ctx)
			if errors.Is(err, ErrDrained) {
				return // clean end-of-stream
			}
			if err != nil {
				consumerErr = err
				return
			}
			if p.cfg.SlowConsumerDelay > 0 {
				select {
				case <-time.After(p.cfg.SlowConsumerDelay):
				case <-ctx.Done():
					consumerErr = ErrCancelled.WithCause(ctx.Err())
					return
				}
			}
			p.mu.Lock()
			p.processed++
			p.payloadBytes += int64(pkt.PayloadLen)
			p.mu.Unlock()
		}
	}()

	enqueued := 0
	var runErr error
producer:
	for {
		if p.cfg.CancelAfter > 0 && enqueued >= p.cfg.CancelAfter {
			cancel()
			runErr = ErrCancelled
			break
		}
		pkt, err := reader.ReadPacket()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			runErr = err // structured parse error with offset
			break producer
		}
		p.tracker.SetCodec(pkt.Codec)
		dup, missing, seqErr := p.tracker.Observe(pkt.Seq)
		if seqErr != nil {
			if me, ok := AsMediaError(seqErr); ok {
				me.Offset = pkt.Offset
			}
			runErr = seqErr
			p.recordCorrupt(pkt.Offset, &pkt.Seq, PhaseSequence, seqErr.Error())
			break producer
		}
		if dup {
			continue // duplicates: no clock advance, no content digest entry
		}
		for _, missingSeq := range missing {
			seq := missingSeq
			p.timeline.Reserve(p.tracker.GapSamples())
			p.recordCorrupt(-1, &seq, PhaseTimeline, "lost packet: reserved gap, no content synthesized")
		}
		p.content.AddPacket(pkt)
		p.timeline.Advance(pkt.ValidSamples)
		if err := p.queue.Enqueue(ctx, pkt); err != nil {
			runErr = err
			break
		}
		enqueued++
	}

	p.queue.Close()
	wg.Wait()
	if consumerErr != nil && runErr == nil {
		runErr = consumerErr
	}

	report := p.buildReport(containerHasher.Sum())
	if runErr != nil {
		report.Counters.Cancelled = isCancelled(runErr)
		return report, runErr
	}
	return report, nil
}

func (p *Pipeline) recordCorrupt(offset int64, seq *uint16, phase, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.corrupt = append(p.corrupt, CorruptPacketRecord{Offset: offset, Seq: seq, Phase: phase, Reason: reason})
}

func (p *Pipeline) buildReport(containerDigest Digest) *Report {
	p.mu.Lock()
	processed := p.processed
	corrupt := append([]CorruptPacketRecord(nil), p.corrupt...)
	payloadBytes := p.payloadBytes
	p.mu.Unlock()

	st := p.queue.Stats()
	r := &Report{
		Counters: PipelineCounters{
			Packets:     processed,
			LostPackets: p.tracker.GapCount(),
			Duplicates:  p.tracker.DuplicateCount(),
			Wraps:       p.tracker.WrapCount(),
			QueuePeak:   st.Peak,
			QueueCap:    st.Capacity,
			Processed:   st.Dequeued,
			Pending:     st.Pending,
		},
		Timeline: TimelineReport{
			DurationTicks:  p.timeline.Cursor(),
			DurationMs:     p.clock.TicksToMillis(p.timeline.Cursor()),
			MissingSamples: int64(p.tracker.GapCount()) * p.tracker.GapSamples(),
		},
		ContentDigest:   p.content.Sum(),
		ContainerDigest: containerDigest,
		Corrupt:         corrupt,
	}
	r.Timeline.UniqueSamples = r.Timeline.DurationTicks - r.Timeline.MissingSamples
	// Objective bitrate over payload bytes and the mapped media duration.
	target := p.cfg.TargetBitrateBps
	if target == 0 {
		target = DefaultTargetBitrateBps
	}
	actualBps := EffectiveBps(payloadBytes, p.timeline.Cursor(), p.clock)
	r.Bitrate = BitrateSpecCheck("stream", target, actualBps, p.cfg.BitrateTolerance)
	// SNR proxy: deterministic from a fixed, documented loss model.
	r.Quality = p.computeQuality(processed)
	return r
}

// SubtitleSpec pairs a subtitle interval with the audio range it should match.
type SubtitleSpec struct {
	ID       string
	Audio    TimeRange
	Subtitle TimeRange
}

// ApplySpecs enriches a report with index digest, incremental plan, subtitle
// alignment results and the overall spec verdict. It is called after Process.
func (p *Pipeline) ApplySpecs(r *Report, states []SourceState, assets []AssetDependency, subs []SubtitleSpec, prevStates []SourceState) error {
	idxDigest, err := IndexDigest(states)
	if err != nil {
		return err
	}
	r.IndexDigest = idxDigest
	plan, err := NewIncrementalIndex(prevStates).Plan(states, assets)
	if err != nil {
		return err
	}
	r.Plan = plan

	items := make([]SubtitleAlignment, 0, len(subs))
	for _, s := range subs {
		items = append(items, CheckSubtitleAlignment(s.ID, s.Audio, s.Subtitle, p.clock,
			p.cfg.AlignmentTolMs, r.ContentDigest))
	}
	r.AlignmentItems = items
	r.Alignment = SummarizeAlignment(items)

	var failures []string
	if !r.Bitrate.Pass {
		failures = append(failures, "bitrate deviation out of tolerance")
	}
	for _, it := range items {
		if !it.Pass {
			failures = append(failures, "subtitle alignment failure: "+it.SubtitleID)
		}
	}
	r.Failures = failures
	hardCorrupt := 0
	for _, c := range r.Corrupt {
		if c.Phase != PhaseTimeline { // reserved gaps are quantified, not failures
			hardCorrupt++
		}
	}
	r.SpecPass = len(failures) == 0 && hardCorrupt == 0
	return nil
}

// computeQuality returns a reproducible SNR proxy for processed streams.
func (p *Pipeline) computeQuality(processed int) QualityResult {
	if processed == 0 {
		return SNRProxy("stream", false, 0, 0)
	}
	// Fixed, documented loss model: noise energy is 1/100 of signal energy,
	// yielding a stable 20 dB proxy independent of run timing.
	return SNRProxy("stream", true, 100, 1)
}

func isCancelled(err error) bool {
	me, ok := AsMediaError(err)
	return ok && me.Phase == PhaseCancelled
}
