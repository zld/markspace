// Package pipeline orchestrates the five acceptance stages over one fixture:
// probe, metadata, audio, stream and manifest. It owns resume planning, cache
// interaction and the counters the CLI reports.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/zld/media-regress/audio"
	"github.com/zld/media-regress/checkpoint"
	"github.com/zld/media-regress/internal/error"
	"github.com/zld/media-regress/manifest"
	"github.com/zld/media-regress/media"
	"github.com/zld/media-regress/stream"
)

// Options configures a run.
type Options struct {
	// Source names the input for reporting and cache identity.
	Source string
	Data   []byte

	SampleRate uint32
	Layout     string
	Loudness   float64

	MaxBlocks int
	MaxBytes  int
	// SlowConsumerMs delays the consumer by this many milliseconds per block.
	SlowConsumerMs int

	// InterruptAfterBlock stops after committing this 1-based block.
	InterruptAfterBlock int
	// Resume continues from the checkpoint at CheckpointPath.
	Resume bool
	// CheckpointPath enables checkpoint persistence when non-empty.
	CheckpointPath string

	// CacheDir enables the content cache when non-empty.
	CacheDir string
	// TamperCache damages one cached entry before reading it back.
	TamperCache bool

	// DropSegment omits a 1-based manifest segment to simulate a gap.
	DropSegment int
}

// Result is the outcome of a run, populated even on failure so the CLI can
// report where processing stopped.
type Result struct {
	Manifest   *manifest.Manifest
	Checkpoint *checkpoint.File
	Resume     checkpoint.Resume
	Summary    manifest.Summary
	Asset      *media.Asset
	// CacheStats mirrors the cache counters when a cache is enabled.
	CacheStats *CacheStats
}

// CacheStats is a copy of the cache counters for reporting.
type CacheStats struct {
	Hits     int
	Misses   int
	Rejected int
	Stored   int
}

// Defaults fills unset options.
func (o *Options) Defaults() {
	if o.SampleRate == 0 {
		o.SampleRate = 48000
	}
	if o.Layout == "" {
		o.Layout = "stereo"
	}
	if o.MaxBlocks == 0 {
		o.MaxBlocks = 4
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = 1 << 20
	}
}

// Run executes the pipeline. A returned error is structured (stage, track,
// offset) whenever it originates inside a stage.
func Run(opts Options) (*Result, error) {
	opts.Defaults()
	res := &Result{Summary: manifest.Summary{ResumeBlock: -1}}
	sum := sha256.Sum256(opts.Data)
	inputDigest := hex.EncodeToString(sum[:])

	// Stage 1: probe and parse the container.
	asset, err := media.Parse(opts.Data, opts.Source)
	if err != nil {
		return res, err
	}
	res.Asset = asset
	res.Summary.Stages = 1

	// Stage 2: metadata normalization and resume planning.
	audioTrack := findAudio(asset)
	if audioTrack == nil {
		return res, merr.New(merr.StageMetadata, merr.KindInconsistent, 0, 0, "asset has no audio track")
	}
	total := len(audioTrack.Samples)
	res.Summary.Stages = 2

	run := runState{
		opts:        opts,
		res:         res,
		asset:       asset,
		track:       audioTrack,
		inputDigest: inputDigest,
		total:       total,
	}
	startBlock, err := run.openCheckpoint()
	if err != nil {
		return res, err
	}
	run.startBlock = startBlock
	if opts.InterruptAfterBlock != 0 && !opts.Resume {
		run.interruptAt = opts.InterruptAfterBlock
	}

	// Stage 3: configure audio conversion and measure the loudness gain.
	conv, err := audio.NewConverter(audio.Config{
		SourceRate:   audioTrack.Audio.SampleRate,
		SourceLayout: audioTrack.Audio.Layout,
		TargetRate:   opts.SampleRate,
		TargetLayout: opts.Layout,
		LoudnessLUFS: opts.Loudness,
	})
	if err != nil {
		return res, err
	}
	if err := measureGain(conv, audioTrack); err != nil {
		return res, err
	}
	run.conv = conv

	// Open the bounded pipeline used to hand blocks to persistence.
	p, err := stream.NewPipeline(stream.Config{MaxBlocks: opts.MaxBlocks, MaxBytes: opts.MaxBytes}, nil)
	if err != nil {
		return res, err
	}
	run.pipeline = p

	builder, err := run.buildManifest()
	if err != nil {
		return res, err
	}
	res.Manifest = builder.Manifest()

	// Stage 4: convert, cache and checkpoint every block.
	res.Summary.Stages = 3
	if err := run.processBlocks(builder); err != nil {
		return res, err
	}
	res.Summary.Stages = 4

	// Stage 5: finish and verify the manifest.
	builder.SetAudioWindow()
	if err := builder.Finish(res.Summary); err != nil {
		return res, err
	}
	res.Summary.Stages = 5
	m := builder.Manifest()
	// Finish already folded the builder's conflict count into the summary;
	// adopt it so the caller sees the same numbers the manifest carries.
	res.Summary = m.Summary
	res.Summary.Stages = 5
	m.Summary = res.Summary
	if err := m.Finalize(); err != nil {
		return res, err
	}
	res.Manifest = m
	if run.cp != nil {
		run.cp.Record(checkpoint.StageState{
			Stage: checkpoint.StageManifest, InputDigest: inputDigest,
			CommittedBlock: total - 1, OutputDigest: m.Digest,
		})
		if err := run.cp.Save(); err != nil {
			return res, err
		}
	}
	if run.store != nil {
		res.CacheStats = &CacheStats{
			Hits:     run.store.Stats().Hits,
			Misses:   run.store.Stats().Misses,
			Rejected: run.store.Stats().Rejected,
			Stored:   run.store.Stats().Stored,
		}
	}
	if run.cp != nil {
		res.Checkpoint = run.cp.File()
	}
	return res, nil
}

func findAudio(a *media.Asset) *media.Track {
	for _, t := range a.Tracks {
		if t.Kind == media.KindAudio {
			return t
		}
	}
	return nil
}

func measureGain(c *audio.Converter, t *media.Track) error {
	if c.TargetLUFS == 0 {
		return nil
	}
	blocks := make([][]byte, len(t.Samples))
	for i, s := range t.Samples {
		blocks[i] = s.Data
	}
	lufs, err := c.MeasureLoudness(blocks)
	if err != nil {
		return err
	}
	return c.ApplyGain(lufs)
}

func formatLoudness(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
func formatGain(v float64) string     { return strconv.FormatFloat(v, 'f', 2, 64) }
