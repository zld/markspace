package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"mediapipe/internal/audio"
	"mediapipe/internal/fingerprint"
	"mediapipe/internal/model"
	"mediapipe/internal/report"
	"mediapipe/internal/segment"
)

type Options struct {
	OutDir              string
	InputPath           string
	TargetBitrateBps    int
	SegmentMs           int64
	CheckpointPackets   int
	ReorderWindow       int
	SilenceWindowMs     int64
	SilenceThreshold    float64
	FingerprintWindowMs int64
	TimeToleranceMs     int64
	Resume              bool
	CheckpointHook      func(processedPackets int64) error
}

func DefaultOptions(outDir string) Options {
	return Options{
		OutDir:              outDir,
		InputPath:           filepath.Join(outDir, "input.mps"),
		TargetBitrateBps:    64000,
		SegmentMs:           2000,
		CheckpointPackets:   128,
		ReorderWindow:       32,
		SilenceWindowMs:     20,
		SilenceThreshold:    300,
		FingerprintWindowMs: 200,
		TimeToleranceMs:     20,
	}
}

type Result struct {
	Manifest          *report.Manifest
	ResumedFromPacket int64
	CheckpointPacket  int64
	CheckpointSource  string
	IndexRecovered    bool
}

func (o *Options) normalize() {
	d := DefaultOptions(o.OutDir)
	if o.InputPath == "" {
		o.InputPath = d.InputPath
	}
	if o.TargetBitrateBps == 0 {
		o.TargetBitrateBps = d.TargetBitrateBps
	}
	if o.SegmentMs == 0 {
		o.SegmentMs = d.SegmentMs
	}
	if o.CheckpointPackets == 0 {
		o.CheckpointPackets = d.CheckpointPackets
	}
	if o.ReorderWindow == 0 {
		o.ReorderWindow = d.ReorderWindow
	}
	if o.SilenceWindowMs == 0 {
		o.SilenceWindowMs = d.SilenceWindowMs
	}
	if o.SilenceThreshold == 0 {
		o.SilenceThreshold = d.SilenceThreshold
	}
	if o.FingerprintWindowMs == 0 {
		o.FingerprintWindowMs = d.FingerprintWindowMs
	}
	if o.TimeToleranceMs == 0 {
		o.TimeToleranceMs = d.TimeToleranceMs
	}
}

func Run(ctx context.Context, opts Options) (*Result, error) {
	opts.normalize()
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return nil, err
	}
	prepared, err := PrepareInput(opts.InputPath, opts.ReorderWindow, 20)
	if err != nil {
		return nil, err
	}

	var cp *Checkpoint
	var checkpointSource string
	var resumedFrom int64
	indexRecovered := false
	if opts.Resume {
		cp, checkpointSource, err = LoadLatestCheckpoint(opts.OutDir)
		if err != nil {
			return nil, err
		}
		if cp.InputSHA256 != prepared.InputSHA {
			return nil, model.NewError(model.ErrorDigest, -1,
				fmt.Sprintf("checkpoint digest %s does not match input %s", cp.InputSHA256, prepared.InputSHA), nil)
		}
		resumedFrom = cp.ProcessedPackets
		opts.TargetBitrateBps = cp.TargetBitrateBps
		opts.SegmentMs = cp.SegmentMs
		opts.CheckpointPackets = cp.CheckpointPackets
		opts.ReorderWindow = cp.ReorderWindow
		opts.SilenceWindowMs = cp.SilenceWindowMs
		opts.FingerprintWindowMs = cp.FingerprintWindowMs
		opts.TimeToleranceMs = cp.TimeToleranceMs
		if _, ierr := segment.ReadMP4Index(indexPath(opts.OutDir)); ierr != nil {
			if err := RecoverIndexFromCheckpoint(opts.OutDir, cp); err != nil {
				return nil, err
			}
			indexRecovered = true
		}
	}
	if resumedFrom > int64(len(prepared.Packets)) {
		return nil, model.NewError(model.ErrorCheckpoint, -1, "checkpoint is ahead of input", nil)
	}

	cutter := segment.NewCutter(opts.SegmentMs)
	var segments []model.Segment
	var fingerprints []model.WindowFingerprint
	var mappings []model.DedupeMapping
	silenceWindows, unknownWindows, voicedWindows := 0, 0, 0
	deduper := fingerprint.NewDeduper(opts.TimeToleranceMs)
	lastCheckpoint := resumedFrom

	if cp != nil {
		segments = append(segments, cp.Segments...)
		fingerprints = append(fingerprints, cp.Fingerprints...)
		mappings = append(mappings, cp.Mappings...)
		silenceWindows = cp.SilenceWindows
		unknownWindows = cp.UnknownWindows
		voicedWindows = cp.VoicedWindows
		deduper = fingerprint.NewDeduperFromState(cp.Segments, cp.Fingerprints, cp.Mappings, opts.TimeToleranceMs)
		for i := int64(0); i < resumedFrom; i++ {
			_ = cutter.Push(prepared.Packets[i])
		}
	}

	detector := audio.NewDetector(opts.SilenceWindowMs, opts.SilenceThreshold)
	handleClosed := func(closed *segment.ClosedSegment) {
		closedWindows := detector.Detect(closed.Segment, closed.Packets)
		for _, w := range closedWindows {
			switch w.State {
			case model.WindowSilent:
				silenceWindows++
			case model.WindowUnknown:
				unknownWindows++
			case model.WindowVoiced:
				voicedWindows++
			}
		}
		closedFingerprints := fingerprint.HashWindows(closed.Segment, closed.Packets, opts.FingerprintWindowMs)
		fingerprints = append(fingerprints, closedFingerprints...)
		updated, _ := deduper.Add(closed.Segment, closedFingerprints)
		closed.Segment = updated
		segments = append(segments, updated)
	}

	processed := resumedFrom
	for i := resumedFrom; i < int64(len(prepared.Packets)); i++ {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
		}
		if closed := cutter.Push(prepared.Packets[i]); closed != nil {
			handleClosed(closed)
		}
		processed++
		if opts.CheckpointPackets > 0 && processed%int64(opts.CheckpointPackets) == 0 {
			checkpoint := &Checkpoint{
				Version: CheckpointVersion, InputPath: opts.InputPath, InputSHA256: prepared.InputSHA,
				ProcessedPackets: processed, TargetBitrateBps: opts.TargetBitrateBps,
				SegmentMs: opts.SegmentMs, CheckpointPackets: opts.CheckpointPackets,
				ReorderWindow: opts.ReorderWindow, SilenceWindowMs: opts.SilenceWindowMs,
				FingerprintWindowMs: opts.FingerprintWindowMs, TimeToleranceMs: opts.TimeToleranceMs,
				Segments: segments, Fingerprints: fingerprints,
				Mappings: deduper.MappingsSnapshot(), SilenceWindows: silenceWindows,
				UnknownWindows: unknownWindows, VoicedWindows: voicedWindows,
				Health: prepared.Health, MaxAlignmentErrorMs: cutter.MaxAlignmentMs,
			}
			if err := SaveCheckpoint(opts.OutDir, checkpoint); err != nil {
				return nil, err
			}
			lastCheckpoint = processed
			if opts.CheckpointHook != nil {
				if err := opts.CheckpointHook(processed); err != nil {
					return &Result{CheckpointPacket: processed, CheckpointSource: checkpointSource}, err
				}
			}
		}
	}

	if tail := cutter.Finish(prepared.Health.Truncated); tail != nil {
		handleClosed(tail)
	}
	mappings = deduper.MappingsSnapshot()

	summary := report.Summary{
		BitrateTargetBps:  opts.TargetBitrateBps,
		PacketLost:        prepared.Health.PacketLost,
		PacketDuplicate:   prepared.Health.PacketDuplicate,
		PacketLate:        prepared.Health.PacketLate,
		PacketCorrupt:     prepared.Health.PacketCorrupt,
		RTCPInvalid:       prepared.Health.RTCPInvalid,
		RTCPStaleReports:  prepared.Health.RTCPStaleReports,
		RTCPMissing:       prepared.Health.RTCPMissing,
		SilenceWindows:    silenceWindows,
		UnknownWindows:    unknownWindows,
		VoicedWindows:     voicedWindows,
		ResumedFromPacket: resumedFrom,
		CheckpointPacket:  lastCheckpoint,
		Truncated:         prepared.Health.Truncated,
	}
	manifest := &report.Manifest{
		SchemaVersion: "mediapipe/v1", Summary: summary,
		InputPath: opts.InputPath, InputSHA256: prepared.InputSHA, InputBytes: prepared.InputBytes,
		SegmentMs: opts.SegmentMs, CheckpointPackets: opts.CheckpointPackets, ReorderWindow: opts.ReorderWindow,
		Segments: segments, Issues: prepared.Health.Issues,
		Fingerprints: fingerprints, DedupeMappings: mappings,
	}
	if _, err := report.WriteManifest(opts.OutDir, manifest); err != nil {
		return nil, err
	}
	finalIndex := segment.Index{
		Version: segment.IndexFormatVersion, InputSHA256: prepared.InputSHA,
		ProcessedPacket: processed, Complete: true, Segments: segments, Mappings: mappings,
	}
	if err := segment.WriteMP4Index(indexPath(opts.OutDir), finalIndex); err != nil {
		return nil, err
	}
	return &Result{
		Manifest: manifest, ResumedFromPacket: resumedFrom, CheckpointPacket: lastCheckpoint,
		CheckpointSource: checkpointSource, IndexRecovered: indexRecovered,
	}, nil
}
