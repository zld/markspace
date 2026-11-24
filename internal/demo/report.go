package demo

import (
	"path/filepath"

	"mediad/internal/audio"
	"mediad/internal/batch"
	"mediad/internal/clock"
	"mediad/internal/manifest"
	"mediad/internal/mederr"
)

// WriteManifest measures committed WAV chunks, computes bitrate/loudness and
// writes the deterministic per-segment manifest.
func (p *Program) WriteManifest(idx *batch.Index, run batch.RunReport) (manifest.Report, error) {
	targetBPS := int64(p.Opt.TargetRate * p.Layout.Channels * 16)
	meter, err := audio.NewMeter(p.Opt.TargetRate, p.Layout.Channels)
	if err != nil {
		return manifest.Report{}, err
	}
	var totalBytes int64
	chunks := make([]manifest.Chunk, 0, len(idx.Chunks))
	for _, rec := range idx.Chunks {
		totalBytes += rec.Bytes
		chunks = append(chunks, manifest.Chunk{
			Index: rec.Index, StartNs: rec.StartNs, EndNs: rec.EndNs,
			File: rec.File, Bytes: rec.Bytes, SHA256: rec.SHA256,
		})
		buf, err := audio.DecodeWAV16(filepath.Join(p.Opt.Out, rec.File))
		if err != nil {
			return manifest.Report{}, err
		}
		if err := meter.AddBuffer(buf); err != nil {
			return manifest.Report{}, err
		}
	}
	levels := meter.Report()
	durationSec := float64(p.EDL.TotalDuration()) / 1e9
	actualBPS := float64(totalBytes) * 8 / durationSec
	deviation := (actualBPS - float64(targetBPS)) / float64(targetBPS) * 100
	hash, err := p.HashChunkOutputs(idx)
	if err != nil {
		return manifest.Report{}, err
	}

	errs := make([]*mederr.Error, 0, len(p.Errors.Errs)+1)
	errs = append(errs, p.Errors.Errs...)
	rep := manifest.Report{
		OK:                       !p.Errors.HasFatal(),
		BitrateTarget:            targetBPS,
		BitrateActual:            actualBPS,
		BitrateDeviationPct:      deviation,
		PeakDBFS:                 levels.PeakDBFS,
		IntegratedLUFS:           levels.IntegratedLUFS,
		TimelineAlignmentErrorMs: clock.Millis(p.AlignmentErrorNs),
		ChunksCommitted:          len(idx.Chunks),
		ChunksNewlyCommitted:     run.NewlyCommitted,
		ChunksReused:             run.Reused,
		OutputHash:               hash,
		IndexRebuilt:             run.IndexRebuilt,
		TruncatedChunksRedone:    run.Truncated,
		Errors:                   errs,
	}

	segments := make([]manifest.Segment, 0, len(p.EDL.Segments))
	for i, s := range p.EDL.Segments {
		segAlign := clock.MaxSnapErrorNs([]int64{s.StartNanos, s.End()}, p.Opt.TargetRate)
		segments = append(segments, manifest.Segment{
			Index:               i,
			Track:               s.Track,
			StartNs:             s.StartNanos,
			EndNs:               s.End(),
			StartMs:             clock.Millis(s.StartNanos),
			EndMs:               clock.Millis(s.End()),
			DurationMs:          clock.Millis(s.DurationNanos),
			TransitionInMs:      clock.Millis(s.TransitionInNanos),
			TargetBitrate:       targetBPS,
			ActualBitrate:       float64(targetBPS),
			BitrateDeviationPct: 0,
			TimelineAlignmentMs: clock.Millis(segAlign),
			Audio: manifest.AudioParams{
				TargetRate:     p.Opt.TargetRate,
				Channels:       p.Layout.Channels,
				Layout:         p.Opt.LayoutName,
				BitsPerSample:  16,
				LoudnessTarget: p.Opt.LoudnessTarget,
				PeakCeilingDB:  p.Opt.PeakCeilingDB,
			},
		})
	}
	m := &manifest.Manifest{
		SchemaVersion: 1,
		Seed:          p.Opt.Seed,
		Fingerprint:   p.Fingerprint,
		ChunkNs:       p.Opt.Chunk.Nanoseconds(),
		TotalStartNs:  0,
		TotalEndNs:    p.EDL.TotalDuration(),
		Audio: manifest.AudioParams{
			TargetRate:     p.Opt.TargetRate,
			Channels:       p.Layout.Channels,
			Layout:         p.Opt.LayoutName,
			BitsPerSample:  16,
			LoudnessTarget: p.Opt.LoudnessTarget,
			PeakCeilingDB:  p.Opt.PeakCeilingDB,
		},
		Segments: segments,
		Chunks:   chunks,
	}
	if _, err := manifest.WriteFile(p.Opt.Out, "manifest.json", m); err != nil {
		return rep, err
	}
	return rep, nil
}
