// Package pipeline wires manifest parsing, RTP recovery, the timeline
// engine, metadata normalization and alignment into one deterministic
// end-to-end run, and verifies cross-stage invariants before publishing.
package pipeline

import (
	"mediadist/manifest"
	"mediadist/media"
	"mediadist/timeline"
	"mediadist/transport"
)

// Limits keep every fixture bounded.
const (
	MaxPackets         = 10000
	MaxSummarySegments = 5000
	alignmentLimitMs   = 20
)

// Config controls one deterministic demo run.
type Config struct {
	Rates          int
	LossRate       float64
	LateMs         int
	ReplayWindowMs int
	CorruptScale   int // 0 means "inject a zero timescale"
	Seed           int64
}

// Metrics is the terminal, verifiable run summary.
type Metrics struct {
	SyncErrorMs            float64
	LossRecoveryRate       float64
	P95LatencyMs           float64
	AlignedRepresentations int
	ManifestSHA256         string
	PacketsReceived        int
	PacketsMissing         int
	PacketsRecovered       int
	Unrecoverable          int
	Replayed               int
	ColorConflicts         int
	ColorMissing           int
	PublishedSegments      int
}

// Pipeline holds the assembled subsystems for one run.
type Pipeline struct {
	cfg       Config
	engine    *timeline.Engine
	jitter    *transport.JitterBuffer
	audio     *media.AudioNormalizer
	color     *media.ColorMerger
	aligner   *manifest.Aligner
	stream    transport.Stream
	latencies []int
}

// New constructs the pipeline and validates configuration.
func New(cfg Config) (*Pipeline, error) {
	if cfg.Rates < 1 || cfg.Rates > manifest.MaxRepresentations {
		return nil, timeline.NewError("INVALID_CONFIG", "rate count out of range",
			map[string]any{"rates": cfg.Rates})
	}
	if cfg.LossRate < 0 || cfg.LossRate > 1 {
		return nil, timeline.NewError("INVALID_CONFIG", "loss rate must be in [0,1]",
			map[string]any{"loss": cfg.LossRate})
	}
	if cfg.ReplayWindowMs < 0 {
		return nil, timeline.NewError("INVALID_CONFIG", "replay window must be >= 0", nil)
	}
	window := timeline.MustNewRat(int64(cfg.ReplayWindowMs), 1)
	eng, err := timeline.NewEngine(window)
	if err != nil {
		return nil, err
	}
	jb, err := transport.NewJitterBuffer(
		transport.Stream{SSRC: 0xC0FFEE, ClockRate: 90000}, 64)
	if err != nil {
		return nil, err
	}
	audio, err := media.NewAudioNormalizer(48000)
	if err != nil {
		return nil, err
	}
	aligner, err := manifest.NewAligner(alignmentLimitMs)
	if err != nil {
		return nil, err
	}
	return &Pipeline{
		cfg:     cfg,
		engine:  eng,
		jitter:  jb,
		audio:   audio,
		color:   media.NewColorMerger([]string{"origin", "edge-a", "edge-b"}),
		aligner: aligner,
		stream:  transport.Stream{SSRC: 0xC0FFEE, ClockRate: 90000},
	}, nil
}

// Run executes the full pipeline and returns the verifiable summary.
func (p *Pipeline) Run() (*Metrics, error) {
	mpdText, m3u8Text, packets, colorMetas, err := GenerateFixtures(p.cfg)
	if err != nil {
		return nil, err
	}
	if len(packets) > MaxPackets {
		return nil, timeline.NewError("MANIFEST_LIMIT", "packet fixture too large",
			map[string]any{"packets": len(packets), "limit": MaxPackets})
	}

	// Stage 1: manifests feed the rational timeline.
	mpd, err := manifest.ParseMPD(mpdText)
	if err != nil {
		return nil, err
	}
	playlist, err := manifest.ParseM3U8(m3u8Text)
	if err != nil {
		return nil, err
	}
	report, err := p.aligner.Align(mpd.Representations)
	if err != nil {
		return nil, err
	}
	if _, err := p.aligner.AlignPlaylist(playlist.Variants); err != nil {
		return nil, err
	}

	// Stage 2: RTP recovery feeds the same engine.
	snapshot := p.engine.Snapshot()
	if err := p.ingestRTP(packets); err != nil {
		// Corrupting failure: replay deterministically from the snapshot.
		p.engine.Restore(snapshot)
		return nil, err
	}

	// Stage 3: metadata normalization emits fragment results.
	colorReport, err := p.color.Merge(colorMetas)
	if err != nil {
		return nil, err
	}
	if err := p.normalizeAudio(mpd); err != nil {
		return nil, err
	}

	// Stage 4: cross-stage invariants before publication.
	if err := p.verifyInvariants(mpd, report); err != nil {
		return nil, err
	}

	summary := p.buildMetrics(mpd, report, colorReport, packets)
	summary.ManifestSHA256 = HashManifest(mpdText, m3u8Text, p.engine.Digest())
	return summary, nil
}

func (p *Pipeline) ingestRTP(packets []PlannedPacket) error {
	// Map extended sequence -> late flag so packets released by the
	// jitter buffer still carry their arrival-lateness contract.
	lateExts := map[uint64]bool{}
	for _, plan := range packets {
		if plan.Kind == KindRecoverableLate {
			ext := transport.ExtSeq(0, packets[0].Packet.SequenceNumber,
				plan.Packet.SequenceNumber)
			lateExts[ext] = true
		}
	}
	for _, plan := range packets {
		if plan.Kind == KindPermanentLoss {
			continue // never fabricate content for an unrecoverable hole
		}
		res, err := p.jitter.Push(plan.Packet)
		if err != nil {
			if timeline.CodeOf(err) == "RTP_UNRECOVERABLE_LATE" {
				continue
			}
			return err
		}
		if res.Duplicate {
			continue
		}
		for _, pkt := range res.Output {
			ext := transport.ExtSeq(0, packets[0].Packet.SequenceNumber, pkt.SequenceNumber)
			ev, err := p.stream.Event(pkt, 0, ext)
			if err != nil {
				return err
			}
			if lateExts[ext] {
				// Arrival lateness is expressed as an event-time offset;
				// the engine's bounded window is the sole enforcement point.
				shifted, serr := ev.Time.Add(timeline.MustNewRat(-int64(p.cfg.LateMs), 1))
				if serr != nil {
					return serr
				}
				ev.Time = shifted
			}
			if err := p.engine.Push(ev); err != nil {
				return err
			}
		}
	}
	out, missing := p.jitter.Flush()
	for _, pkt := range out {
		ext := transport.ExtSeq(0, packets[0].Packet.SequenceNumber, pkt.SequenceNumber)
		ev, err := p.stream.Event(pkt, 0, ext)
		if err != nil {
			return err
		}
		if lateExts[ext] {
			shifted, serr := ev.Time.Add(timeline.MustNewRat(-int64(p.cfg.LateMs), 1))
			if serr != nil {
				return serr
			}
			ev.Time = shifted
		}
		if err := p.engine.Push(ev); err != nil {
			return err
		}
	}
	_ = missing
	return nil
}

func (p *Pipeline) normalizeAudio(mpd *manifest.MPD) error {
	for i, rep := range mpd.Representations {
		channels := 2
		if i == len(mpd.Representations)-1 {
			channels = 6 // one surround representation to exercise layout mapping
		}
		res, err := p.audio.Normalize(media.AudioMeta{
			Source:     rep.ID,
			Channels:   channels,
			SampleRate: 48000,
		})
		if err != nil {
			return err
		}
		if res.Failed() {
			return res.Error
		}
	}
	return nil
}

func (p *Pipeline) verifyInvariants(mpd *manifest.MPD, rep *manifest.BoundaryReport) error {
	committed := p.engine.Committed()
	for i := 1; i < len(committed); i++ {
		if committed[i].Epoch < committed[i-1].Epoch {
			return timeline.NewError("INVARIANT_EPOCH_ORDER",
				"committed epochs are not monotonic",
				map[string]any{"index": i})
		}
		if committed[i].Epoch == committed[i-1].Epoch &&
			committed[i].Time.Cmp(committed[i-1].Time) < 0 {
			return timeline.NewError("INVARIANT_TIME_ORDER",
				"committed event times are not monotonic",
				map[string]any{"index": i})
		}
	}
	// Segments inside a representation must not overlap.
	for _, r := range mpd.Representations {
		for i := 1; i < len(r.Segments); i++ {
			prevEnd, err := r.Segments[i-1].End()
			if err != nil {
				return err
			}
			if r.Segments[i].Start.Cmp(prevEnd) < 0 {
				return timeline.NewError("INVARIANT_OVERLAP",
					"segments overlap",
					map[string]any{"rep": r.ID, "segment": i})
			}
		}
	}
	m := p.jitter.Metrics()
	if m.Recovered > m.Missing {
		return timeline.NewError("INVARIANT_RECOVERY_COUNT",
			"recovered packets exceed missing packets",
			map[string]any{"recovered": m.Recovered, "missing": m.Missing})
	}
	if rep.MaxDevMs > alignmentLimitMs {
		return timeline.NewError("INVARIANT_ALIGNMENT",
			"boundary deviation beyond publication threshold",
			map[string]any{"deviation_ms": rep.MaxDevMs})
	}
	return nil
}

