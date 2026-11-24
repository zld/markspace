package pipeline

import (
	"errors"
	"fmt"
	"time"

	"medcheck/internal/adapter"
	"medcheck/internal/checkpoint"
	"medcheck/internal/clock"
	"medcheck/internal/output"
	"medcheck/internal/sample"
)

// RunDemo executes the deterministic demo, including optional interrupt/resume.
func RunDemo(cfg Config) (*Result, error) {
	loss := append([]int(nil), cfg.Loss...)
	if cfg.Unrecoverable {
		loss = append(loss, 23) // final video delta leaves an unresolved keyframe wait
	}
	s, err := sample.Generate(sample.Config{
		Seed: cfg.Seed, Loss: loss, Reorder: cfg.Reorder,
	})
	if err != nil {
		return nil, err
	}
	if err := validateAdapters(s); err != nil {
		return nil, err
	}

	if cfg.TruncateAt > 0 && cfg.Resume {
		return runInterruptedResume(cfg, s)
	}
	return runOnce(cfg, s)
}

func runOnce(cfg Config, s *sample.Sample) (*Result, error) {
	p := NewProcessor(s)
	for i, pk := range s.Packets {
		p.ProcessPacket(pk)
		if cfg.CheckpointPath != "" && cfg.CheckpointEvery > 0 && (i+1)%cfg.CheckpointEvery == 0 {
			if err := checkpoint.Append(cfg.CheckpointPath, p.Checkpoint(i+1)); err != nil {
				return nil, err
			}
		}
	}
	p.Finish()
	sum, _, err := writeOutput(cfg.OutputPath, p.Document())
	if err != nil {
		return nil, err
	}
	return resultFromProcessor(cfg, s, p, sum, 0, 0), nil
}

func runInterruptedResume(cfg Config, s *sample.Sample) (*Result, error) {
	if cfg.TruncateAt > len(s.Packets) {
		return nil, fmt.Errorf("truncate-at %d exceeds observed packets %d", cfg.TruncateAt, len(s.Packets))
	}

	// A complete in-memory run is the oracle for checkpoint equivalence.
	full := NewProcessor(s)
	processAll(full, s)
	full.Finish()
	fullBytes, fullSHA, err := output.Build(full.Document())
	if err != nil {
		return nil, err
	}
	_ = fullBytes

	every := cfg.CheckpointEvery
	if every <= 0 {
		every = 1
	}
	interrupted := NewProcessor(s)
	lastCheckpointBoundary := 0
	for i := 0; i < cfg.TruncateAt; i++ {
		interrupted.ProcessPacket(s.Packets[i])
		boundary := i + 1
		if every > 0 && boundary%every == 0 {
			if err := checkpoint.Append(cfg.CheckpointPath, interrupted.Checkpoint(boundary)); err != nil {
				return nil, err
			}
			lastCheckpointBoundary = boundary
		}
	}
	// An interrupt always lands on a packet boundary.
	if lastCheckpointBoundary != cfg.TruncateAt {
		if err := checkpoint.Append(cfg.CheckpointPath, interrupted.Checkpoint(cfg.TruncateAt)); err != nil {
			return nil, err
		}
	}
	if cfg.CorruptCheckpoint {
		if err := checkpoint.CorruptLastRecord(cfg.CheckpointPath); err != nil {
			return nil, err
		}
	}

	resumeStart := time.Now()
	loaded, loadErr := checkpoint.LoadLatestValid(cfg.CheckpointPath)
	if loadErr != nil {
		var me clock.MediaError
		if errors.As(loadErr, &me) && me.Code == clock.CodeCheckpointInvalid && loaded.State.Boundary > 0 {
			// Explicitly reported below; nearest valid state is used.
		} else {
			return &Result{ExitCode: 3, ErrorCodes: map[string]int{string(clock.CodeCheckpointNoValidBoundary): 1}}, loadErr
		}
	}

	resumed := NewProcessor(s)
	resumed.Restore(loaded.State)
	start := loaded.State.Boundary
	if start > len(s.Packets) {
		return nil, fmt.Errorf("checkpoint boundary %d exceeds input", start)
	}
	for i := start; i < len(s.Packets); i++ {
		resumed.ProcessPacket(s.Packets[i])
		if every > 0 && (i+1)%every == 0 {
			_ = checkpoint.Append(cfg.CheckpointPath, resumed.Checkpoint(i+1))
		}
	}
	resumed.Finish()
	sum, _, err := writeOutput(cfg.OutputPath, resumed.Document())
	if err != nil {
		return nil, err
	}
	resumeMS := time.Since(resumeStart).Milliseconds()
	if sum != fullSHA {
		return &Result{ExitCode: 4, SHA256: sum}, fmt.Errorf("checkpoint resume checksum %s != full run %s", sum, fullSHA)
	}

	result := resultFromProcessor(cfg, s, resumed, sum, resumeMS, loaded.State.Boundary)
	if loadErr != nil {
		result.ErrorCodes[string(clock.CodeCheckpointInvalid)] = len(loaded.Corrupt)
	}
	return result, nil
}

func processAll(p *Processor, s *sample.Sample) {
	for _, pk := range s.Packets {
		p.ProcessPacket(pk)
	}
}

func writeOutput(path string, doc output.Document) (string, int, error) {
	if path == "" {
		data, sum, err := output.Build(doc)
		return sum, len(data), err
	}
	return output.WriteFile(path, doc)
}

func validateAdapters(s *sample.Sample) error {
	reg := adapter.NewDefaultRegistry()
	aa, err := reg.GetAudio(adapter.VersionAACV1)
	if err != nil {
		return err
	}
	aout, aerrs := aa.Adapt(adapter.AudioInput{
		Channels: s.Audio.Channels, Layout: s.Audio.Layout,
		SampleRate: s.Audio.SampleRate, Target: s.Audio,
	})
	for _, e := range aerrs {
		_ = e // provenance is deterministic; edge-case runner exposes details.
	}
	s.Audio = aout.Spec

	vh, err := reg.GetVideo(adapter.VersionH264V1)
	if err != nil {
		return err
	}
	vout, verrs := vh.Adapt(adapter.VideoInput{Width: s.Video.Width, Height: s.Video.Height, Color: s.Video.Color})
	for _, e := range verrs {
		_ = e
	}
	s.Video = vout.Spec
	return nil
}

func resultFromProcessor(cfg Config, s *sample.Sample, p *Processor, sum string, resumeMS int64, boundary int) *Result {
	exitCode := 0
	if p.ErrorCodes[string(clock.CodeVideoUnrecoverable)] > 0 ||
		p.ErrorCodes[string(clock.CodeAudioGapUnrecoverable)] > 0 {
		exitCode = 2
	}
	return &Result{
		Packets:         p.Ingestor.Stats.Packets,
		Duplicates:      p.Ingestor.Stats.Duplicates,
		Reordered:       p.Ingestor.Stats.Reordered,
		Recovered:       p.Recovery.Stats.Recovered,
		AvDriftMS:       p.Syncer.Stats.AvDriftMS,
		AvCorrections:   p.Syncer.Stats.Corrections,
		FirstBadOffset:  s.FirstBadOffset,
		ResumeMS:        resumeMS,
		SHA256:          sum,
		OutputPath:      cfg.OutputPath,
		CheckpointPath:  cfg.CheckpointPath,
		ErrorCodes:      p.ErrorCodes,
		ResumedBoundary: boundary,
		ExitCode:        exitCode,
	}
}
