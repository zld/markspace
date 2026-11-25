package main

import (
	"bytes"
	"context"
	"fmt"

	"archivecheck/internal/clock"
	"archivecheck/internal/media"
	"archivecheck/internal/pipeline"
	"archivecheck/internal/policy"
	"archivecheck/internal/samplegen"
	"archivecheck/internal/subtitle"
)

// BuildJobs converts generated samples into deterministic pipeline jobs.
func BuildJobs(samples []samplegen.Sample, seed int64) []pipeline.Job {
	jobs := make([]pipeline.Job, 0, len(samples))
	for _, sample := range samples {
		sample := sample
		jobs = append(jobs, pipeline.Job{
			Index:  sample.Index,
			Input:  sample.InputName,
			Sample: sample.Index,
			Work: func(ctx context.Context) (pipeline.Outcome, error) {
				if err := ctx.Err(); err != nil {
					return pipeline.Outcome{}, err
				}
				return processSample(sample, seed)
			},
		})
	}
	return jobs
}

// BarrierAfter makes jobs at or after afterIndex wait for cancellation during
// the first cancellation demonstration run.
func BarrierAfter(jobs []pipeline.Job, afterIndex int) []pipeline.Job {
	out := make([]pipeline.Job, len(jobs))
	copy(out, jobs)
	for i := range out {
		if out[i].Index < afterIndex {
			continue
		}
		out[i].Work = func(ctx context.Context) (pipeline.Outcome, error) {
			select {
			case <-ctx.Done():
				return pipeline.Outcome{}, ctx.Err()
			}
		}
	}
	return out
}

func processSample(s samplegen.Sample, seed int64) (pipeline.Outcome, error) {
	switch s.Kind {
	case "subtitle":
		return processSubtitle(s, seed)
	case "timecode":
		return processTimecode(s, seed)
	case "mp4":
		return processMP4(s, seed)
	case "description":
		return processDescription(s, seed)
	case "rtp":
		return processRTP(s, seed)
	case "srt-truncated":
		_, err := subtitle.ParseSRT(s.Index, s.Data)
		return pipeline.Outcome{InputBytes: len(s.Data)}, err
	case "srt-oversized":
		_, err := subtitle.ParseSRT(s.Index, s.Data)
		return pipeline.Outcome{InputBytes: len(s.Data)}, err
	case "timecode-illegal-df":
		_, err := clock.ParseTimecode(s.Timecode, s.Rate, s.Index, 0)
		return pipeline.Outcome{}, err
	case "rtp-bad":
		_, err := media.ParseRTP(s.Index, 0, s.RTPBytes, s.RTPClockRate, s.RTPExpectedTS)
		return pipeline.Outcome{InputBytes: len(s.RTPBytes)}, err
	default:
		return pipeline.Outcome{}, fmt.Errorf("unknown sample kind %q", s.Kind)
	}
}

func processSubtitle(s samplegen.Sample, seed int64) (pipeline.Outcome, error) {
	var track *subtitle.Track
	var err error
	if bytes.HasPrefix(s.Data, []byte("WEBVTT")) {
		track, err = subtitle.ParseVTT(s.Index, s.Data)
	} else {
		track, err = subtitle.ParseSRT(s.Index, s.Data)
	}
	if err != nil {
		return pipeline.Outcome{InputBytes: len(s.Data)}, err
	}
	delta := int64(0)
	if len(track.Cues) > 0 {
		delta = int64(track.Cues[0].Start - s.ExpectedTimeNS)
	}
	cfg, err := policy.Decide(s.Index, s.Description, s.Quality, s.Constraints)
	if err != nil {
		return pipeline.Outcome{InputBytes: len(s.Data), Details: cfg}, err
	}
	return configOutcome(s, cfg, delta, len(s.Data), seed), nil
}

func processTimecode(s samplegen.Sample, seed int64) (pipeline.Outcome, error) {
	got, err := clock.ParseTimecode(s.Timecode, s.Rate, s.Index, 0)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	cfg, err := policy.Decide(s.Index, s.Description, s.Quality, s.Constraints)
	if err != nil {
		return pipeline.Outcome{Details: cfg}, err
	}
	return configOutcome(s, cfg, int64(got-s.ExpectedTimeNS), 0, seed), nil
}

func processMP4(s samplegen.Sample, seed int64) (pipeline.Outcome, error) {
	meta, err := media.ProbeMP4(s.Index, s.Data)
	if err != nil {
		return pipeline.Outcome{InputBytes: len(s.Data)}, err
	}
	if len(meta.Tracks) == 0 {
		return pipeline.Outcome{InputBytes: len(s.Data)}, fmt.Errorf("probed MP4 has no tracks")
	}
	desc := meta.Tracks[0]
	delta := desc.DurationNS - int64(s.ExpectedTimeNS)
	cfg, err := policy.Decide(s.Index, desc, s.Quality, s.Constraints)
	if err != nil {
		return pipeline.Outcome{InputBytes: len(s.Data), Details: cfg}, err
	}
	return configOutcome(s, cfg, delta, len(s.Data), seed), nil
}

func processDescription(s samplegen.Sample, seed int64) (pipeline.Outcome, error) {
	cfg, err := policy.Decide(s.Index, s.Description, s.Quality, s.Constraints)
	if err != nil {
		return pipeline.Outcome{Details: cfg, EstimatedBytes: cfg.EstimatedBytes}, err
	}
	return configOutcome(s, cfg, 0, 0, seed), nil
}

func processRTP(s samplegen.Sample, seed int64) (pipeline.Outcome, error) {
	parsed, err := media.ParseRTP(s.Index, 0, s.RTPBytes, s.RTPClockRate, s.RTPExpectedTS)
	if err != nil {
		return pipeline.Outcome{InputBytes: len(s.RTPBytes)}, err
	}
	cfg, err := policy.Decide(s.Index, s.Description, s.Quality, s.Constraints)
	if err != nil {
		return pipeline.Outcome{InputBytes: len(s.RTPBytes), Details: cfg}, err
	}
	return configOutcome(s, cfg, int64(parsed.TimeDeltaNS), len(s.RTPBytes), seed), nil
}

func configOutcome(s samplegen.Sample, cfg policy.Config, timeDelta int64, inputBytes int, seed int64) pipeline.Outcome {
	size := int(cfg.EstimatedBytes)
	payload := deterministicBytes(size, seed, s.Index)
	return pipeline.Outcome{
		Output:          s.OutputName,
		OutputBytes:     payload,
		InputBytes:      inputBytes,
		OutputByteCount: len(payload),
		EstimatedBytes:  cfg.EstimatedBytes,
		TimeDeltaNS:     timeDelta,
		ByteDelta:       int64(len(payload)) - cfg.EstimatedBytes,
		Details:         cfg,
	}
}

func deterministicBytes(size int, seed int64, index int) []byte {
	out := make([]byte, size)
	for i := range out {
		out[i] = byte(uint64(seed)*17 + uint64(index)*131 + uint64(i)*31)
	}
	return out
}
