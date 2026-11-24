// Package pipeline wires ingest, reorder, clock and audio into one
// deterministic processing job with checkpoint-backed crash recovery.
package pipeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"math"

	"mediad/internal/audio"
	"mediad/internal/checkpoint"
	"mediad/internal/clock"
	"mediad/internal/reorder"
)

const negInf = -math.MaxFloat64

// Config is the fully-resolved job configuration.
type Config struct {
	InputPath    string  `json:"input_path"`
	SourceRate   int     `json:"source_rate"`
	SourceChans  int     `json:"source_channels"`
	TargetRate   int     `json:"target_rate"`
	TargetLayout string  `json:"target_layout"`
	TargetLUFS   float64 `json:"target_lufs"`
	WindowSize   uint64  `json:"window_size"`
	BurstRun     uint64  `json:"burst_run"`
	BlockFrames  int     `json:"block_frames"`
}

// ConfigJSON renders the canonical parameter digest input. The input path is
// deliberately excluded: it identifies the job but is not a processing
// parameter, so a relocated-but-otherwise-identical input stays compatible.
func (c Config) ConfigJSON() []byte {
	params := struct {
		SourceRate   int     `json:"source_rate"`
		SourceChans  int     `json:"source_channels"`
		TargetRate   int     `json:"target_rate"`
		TargetLayout string  `json:"target_layout"`
		TargetLUFS   float64 `json:"target_lufs"`
		WindowSize   uint64  `json:"window_size"`
		BurstRun     uint64  `json:"burst_run"`
		BlockFrames  int     `json:"block_frames"`
	}{
		c.SourceRate, c.SourceChans, c.TargetRate, c.TargetLayout,
		c.TargetLUFS, c.WindowSize, c.BurstRun, c.BlockFrames,
	}
	b, _ := json.Marshal(params)
	return b
}

// Gap is an unrecoverable loss run reported in the final summary.
type Gap = reorder.Gap

// Report is the verifiable delivery evidence for one completed job.
type Report struct {
	Packets     uint64
	Duplicates  uint64
	Reordered   uint64
	LateDropped uint64
	Gaps        int
	Bursts      int
	SamplesIn   int64
	SamplesOut  int64
	PeakDBFS    float64
	ClipBefore  float64
	ClipAfter   float64
	Clipped     int
	SHA256      string
	ResumeMS    int64
}

// Exit codes mandated by the CLI contract.
const (
	ExitOK             = 0
	ExitCorruptInput   = 2
	ExitCheckpointBad  = 3
	ExitInternalVerify = 4
)

// ErrInterrupted is returned when InterruptAfter stops the run on purpose.
var ErrInterrupted = errors.New("interrupted after block boundary")

// Runner executes one job over an arbitrary byte stream.
type Runner struct {
	cfg       Config
	ckptPath  string
	interrupt int // stop after this many RTP packets at a block boundary; <=0 = never

	timeline *clock.Timeline
	window   *reorder.Window
	resample *audio.Resampler
	dstCh    int

	out        io.Writer
	hasher     hash.Hash
	peak       float64
	clipBefore float64
	clipAfter  float64
	clipped    int
	samplesIn  int64
	samplesOut int64
	packets    uint64

	pcmBuf    []int16      // source-layout frames waiting for a full block
	prefixBuf bytes.Buffer // committed input prefix, retained for resume diff
	state     *checkpoint.State
	blocks    int
	resumeMS  int64

	// Baselines restored from a checkpoint; zero on a fresh run.
	basePackets, baseDup, baseReorder, baseLate uint64
	baseGaps, baseBursts, baseClipped           int
	baseSamplesIn                               int64
	basePeak, baseClipBefore, baseClipAfter     float64
}
