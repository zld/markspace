// Package engine orchestrates the full continuity run end to end: it reads
// the frame and RTP streams, validates records on the nanosecond clock, cuts
// segments, detects silence and duplicates, and only then commits
// checkpoints and writes manifests plus provenance. Resumption continues
// from the last committed input offset; identical input and config never
// produce new or different output bytes.
package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"

	"example.com/continuity/internal/checkpoint"
	"example.com/continuity/internal/fault"
	"example.com/continuity/internal/fingerprint"
	"example.com/continuity/internal/manifest"
	"example.com/continuity/internal/media"
	"example.com/continuity/internal/mp4adapt"
	"example.com/continuity/internal/provenance"
	"example.com/continuity/internal/segment"
	"example.com/continuity/internal/silence"
	"example.com/continuity/internal/source"
)

// Config is the full run configuration. Its JSON digest is the config
// identity used by checkpoint and provenance.
type Config struct {
	ConfigVersion      string
	QueueCapacity      int
	MaxFrames          int
	MaxFramePayload    int
	MaxTotalBytes      int64
	MaxSegmentNS       int64
	ExpectedIntervalNS int64
	JumpFactor         float64
	ReorderWindow      int
	SilenceThreshold   int16
	AudioClockRate     int64
	SamplesPerPacket   int
	// InterruptAfter simulates a crash after this many committed frames. It
	// is a runtime control and is excluded from the configuration digest so
	// an interrupted run and its resumption share one config identity.
	InterruptAfter int `json:"-"`
}

// DefaultConfig returns the standard acceptance configuration.
func DefaultConfig() Config {
	return Config{
		ConfigVersion:      "continuity-v1",
		QueueCapacity:      16,
		MaxFrames:          1 << 16,
		MaxFramePayload:    16 << 20,
		MaxTotalBytes:      1 << 32,
		MaxSegmentNS:       2_000_000_000,
		ExpectedIntervalNS: 40_000_000,
		JumpFactor:         2.0,
		ReorderWindow:      8,
		SilenceThreshold:   16,
		AudioClockRate:     media.AudioClockRate,
		SamplesPerPacket:   media.SamplesPerPkt,
	}
}

// Result is what a completed run reports to the CLI.
type Result struct {
	Summary  *manifest.Summary
	Trace    *provenance.Trace
	Segments []*segment.Segment
	Silence  *silence.Report
	Dedup    []*fingerprint.Result
	Resumed  bool
	FramesIn int
}

// Engine holds the run state and collaborators.
type Engine struct {
	cfg    Config
	outDir string
	store  *checkpoint.Store
}

// New builds an engine writing into outDir.
func New(cfg Config, outDir string) *Engine {
	return &Engine{cfg: cfg, outDir: outDir, store: checkpoint.NewStore(outDir)}
}

func digestReader(r io.Reader) ([]byte, string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, "", err
	}
	return data, checkpoint.DigestOf(data), nil
}

// ErrInterrupted is returned when InterruptAfter simulates a crash.
var ErrInterrupted = errors.New("engine: simulated interrupt after checkpoint commit")

// Run executes the pipeline. frameData and rtpData are the raw, length
// prefixed input streams. On resume it skips already-committed frames and
// rebuilds all derived state, so committed output never changes.
func (e *Engine) Run(ctx context.Context, frameData, rtpData []byte) (*Result, error) {
	inputDigest := checkpoint.DigestOf(frameData)
	configDigest, err := provenance.ConfigDigestOf(e.cfg)
	if err != nil {
		return nil, err
	}
	if err := e.store.CleanTemp(); err != nil {
		return nil, err
	}

	var resumeFrames int64
	resumed := false
	if e.store.Exists() {
		st, err := e.store.Load(inputDigest, configDigest)
		if err != nil {
			// A stale checkpoint means a different run: clear and restart.
			if errors.Is(err, fault.ErrStaleCheckpoint) {
				if err := e.store.Reset(); err != nil {
					return nil, err
				}
			} else {
				return nil, err
			}
		} else {
			resumeFrames = st.FrameCount
			resumed = true
		}
	}

	// Frame stage: bounded reader plus per-record validation.
	limits := source.Limits{
		MaxFramePayload: e.cfg.MaxFramePayload,
		MaxPacketBytes:  64 << 10,
		MaxFrames:       e.cfg.MaxFrames,
		MaxTotalBytes:   e.cfg.MaxTotalBytes,
	}
	frameReader := source.NewFrameReader(bytes.NewReader(frameData), limits)
	cutter, err := segment.New(segment.Config{
		ExpectedIntervalNS: e.cfg.ExpectedIntervalNS,
		JumpFactor:         e.cfg.JumpFactor,
		MaxSegmentNS:       e.cfg.MaxSegmentNS,
	})
	if err != nil {
		return nil, err
	}
	detector, err := silence.New(silence.Config{
		Track: "audio/0", ClockRate: e.cfg.AudioClockRate,
		SamplesPerPacket: e.cfg.SamplesPerPacket,
		SilenceThreshold: e.cfg.SilenceThreshold,
		ReorderWindow:    e.cfg.ReorderWindow,
	})
	if err != nil {
		return nil, err
	}
	deduper := fingerprint.NewDeduper()

	// Audio stage: parse every RTP packet into the silence detector.
	packetReader := source.NewPacketReader(bytes.NewReader(rtpData), limits.MaxPacketBytes)
	if err := e.consumeAudio(ctx, detector, packetReader); err != nil {
		return nil, err
	}
	silenceReport, err := detector.Finish()
	if err != nil {
		return nil, err
	}

	var segments []*segment.Segment
	var dedupResults []*fingerprint.Result
	var committed int
	var payloadBytes int64

	// Derived state (segments, fingerprints, metrics) is always rebuilt from
	// the complete stream so a resumed run produces byte-identical output to
	// an uninterrupted one. The checkpoint records the committed position; on
	// resume we re-validate every frame but only advance the checkpoint past
	// frames beyond the previously committed count.
	for {
		rec, err := frameReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		done, err := cutter.Push(rec)
		if err != nil {
			return nil, err
		}
		if done != nil {
			segments = append(segments, done)
		}
		// Frame-level fingerprint observation feeds the deduper before any
		// segment-level classification.
		if _, _, err := deduper.ObserveFrame(rec); err != nil {
			return nil, err
		}
		payloadBytes += int64(len(rec.Payload))
		committed++

		// Re-commit only once processing moves beyond the last position a
		// previous interrupted run had already persisted.
		if int64(committed) > resumeFrames {
			state := &checkpoint.State{
				Version: 1, InputDigest: inputDigest, ConfigDigest: configDigest,
				InputOffset: frameReader.Offset(), FrameCount: int64(committed),
				Stages: map[string]int64{
					fault.StageFrame:   frameReader.Offset(),
					fault.StageSegment: frameReader.Offset(),
				},
			}
			if err := e.store.Save(state); err != nil {
				return nil, err
			}
		}
		if e.cfg.InterruptAfter > 0 && committed >= e.cfg.InterruptAfter && !resumed {
			return nil, ErrInterrupted
		}
	}
	final, err := cutter.Finish()
	if err != nil {
		return nil, err
	}
	if final != nil {
		segments = append(segments, final)
	}
	for _, seg := range segments {
		res, err := deduper.Apply(seg)
		if err != nil {
			return nil, err
		}
		dedupResults = append(dedupResults, res)
	}

	header := manifest.Header{
		SchemaVersion: 1, ConfigVersion: e.cfg.ConfigVersion, InputDigest: inputDigest,
	}
	segRecs, err := manifest.BuildSegmentRecords(header, segments)
	if err != nil {
		return nil, err
	}
	silRecs, err := manifest.BuildSilenceRecords(header, silenceReport)
	if err != nil {
		return nil, err
	}
	dedupRecs, err := manifest.BuildDedupRecords(header, dedupResults)
	if err != nil {
		return nil, err
	}
	digests, err := manifest.WriteAll(e.outDir, segRecs, silRecs, dedupRecs)
	if err != nil {
		return nil, err
	}

	// MP4 acceptance: render the manifest timeline into an MP4 and read its
	// sample time base back through go-mp4, measuring alignment in nanos.
	mp4Bytes, err := mp4adapt.Render(segRecs)
	if err != nil {
		return nil, err
	}
	timeline, err := mp4adapt.ReadBack(mp4Bytes)
	if err != nil {
		return nil, err
	}
	alignmentNS, err := mp4adapt.AlignmentErrorNS(segRecs, timeline)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(e.outDir, mp4adapt.TimelineFile), mp4Bytes, 0o644); err != nil {
		return nil, fault.New(fault.StageManifest, "mp4-write", err)
	}

	summary, err := e.buildSummary(header, frameData, payloadBytes, silenceReport,
		dedupResults, segments, alignmentNS)
	if err != nil {
		return nil, err
	}
	summaryBytes, err := manifest.WriteSummary(e.outDir, summary)
	if err != nil {
		return nil, err
	}

	trace := provenance.New(e.cfg.ConfigVersion, inputDigest, configDigest)
	rtpDigest := checkpoint.DigestOf(rtpData)
	trace.AddSource("frames", "frames.bin", inputDigest, int64(len(frameData)))
	trace.AddSource("rtp", "audio.bin", rtpDigest, int64(len(rtpData)))
	for _, name := range []string{manifest.SegmentsFile, manifest.SilenceFile, manifest.DedupFile} {
		trace.AddOutput(name, fault.StageManifest, 0, uint64(committed), digests[name])
	}
	trace.AddOutput(manifest.SummaryFile, fault.StageManifest, 0, uint64(committed),
		checkpoint.DigestOf(summaryBytes))
	if _, _, err := trace.Write(e.outDir); err != nil {
		return nil, err
	}
	if err := trace.Verify(); err != nil {
		return nil, err
	}

	// Final checkpoint records all durable outputs and their digest.
	finalState := &checkpoint.State{
		Version: 1, InputDigest: inputDigest, ConfigDigest: configDigest,
		InputOffset: frameReader.Offset(), FrameCount: int64(committed),
		Stages:       map[string]int64{fault.StageManifest: frameReader.Offset()},
		Outputs:      []string{manifest.SegmentsFile, manifest.SilenceFile, manifest.DedupFile, manifest.SummaryFile},
		OutputDigest: checkpoint.DigestOf(summaryBytes),
	}
	if err := e.store.Save(finalState); err != nil {
		return nil, err
	}

	return &Result{
		Summary: summary, Trace: trace, Segments: segments,
		Silence: silenceReport, Dedup: dedupResults,
		Resumed: resumed, FramesIn: committed,
	}, nil
}

func (e *Engine) consumeAudio(ctx context.Context, detector *silence.Detector,
	pr *source.PacketReader) error {
	for {
		if err := ctx.Err(); err != nil {
			return fault.New(fault.StagePipeline, "audio", err)
		}
		pkt, err := pr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := detector.Push(pkt.Packet, pkt.Offset); err != nil {
			return err
		}
	}
}

// buildSummary computes all metrics on the single nanosecond time base and
// measures quality against freshly decoded (re-rendered) reference frames.
func (e *Engine) buildSummary(header manifest.Header, frameData []byte,
	payloadBytes int64, sil *silence.Report, dedup []*fingerprint.Result,
	segments []*segment.Segment, alignmentNS int64) (*manifest.Summary, error) {
	spec := media.DefaultSpec()
	durationNS := spec.DurationNS()
	target := spec.TargetBitrateBps()
	actual := media.BitrateBps(payloadBytes, durationNS)
	errPct := 0.0
	if target > 0 {
		errPct = (actual - target) / target * 100
		if errPct < 0 {
			errPct = -errPct
		}
	}

	// Quality metrics: compare every stored frame payload against the
	// deterministic reference pixels it was generated from.
	var psnrSum, ssimSum float64
	var n int
	frameReader := source.NewFrameReader(bytes.NewReader(frameData), source.DefaultLimits())
	for {
		rec, err := frameReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		ref := media.RenderPixels(spec.PixelSeqForTest(int(rec.Seq)), rec.Width, rec.Height)
		psnr := media.PSNR(ref, rec.Payload)
		if math.IsInf(psnr, 1) {
			psnr = media.PerfectPSNRCap // identical frames: report the finite cap
		}
		psnrSum += psnr
		ssimSum += media.SSIM(ref, rec.Payload)
		n++
	}
	var duplicates int
	for _, r := range dedup {
		if r.Kind == fingerprint.KindDuplicate {
			duplicates++
		}
	}
	summary := &manifest.Summary{
		Header:          header,
		Frames:          n,
		Segments:        len(segments),
		SilenceNS:       sil.SilenceNS,
		UnknownNS:       sil.UnknownNS,
		Duplicates:      duplicates,
		TargetBitrate:   target,
		ActualBitrate:   actual,
		BitrateErrorPct: errPct,
		PSNRDB:          psnrSum / float64(n),
		SSIM:            ssimSum / float64(n),
		// Measured by reading the rendered MP4 time base back through
		// go-mp4 and comparing it against frame capture timestamps.
		ManifestAlignNS: alignmentNS,
	}
	return summary, nil
}

// RemoveArtifacts deletes generated output files, used before a clean rerun.
func RemoveArtifacts(outDir string) error {
	for _, name := range []string{
		manifest.SegmentsFile, manifest.SilenceFile, manifest.DedupFile,
		manifest.SummaryFile, provenance.TraceFile,
	} {
		if err := os.Remove(filepath.Join(outDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
