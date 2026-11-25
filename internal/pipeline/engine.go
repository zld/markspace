// Package pipeline wires the media subsystems into one bounded, resumable
// processing run: synthesized WAV chunks traverse an RTP transport with
// reorder/duplicate handling, a streaming PCM decoder, a validated channel
// mapper and a bounded backpressure queue, then land as hash-verified MP4
// segments committed through the local checkpoint journal.
package pipeline

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"mediad/internal/clock"
	"mediad/internal/fault"
	"mediad/internal/index"
	"mediad/internal/maptrack"
	"mediad/internal/recover"
	"mediad/internal/rtp"
	"mediad/internal/safeio"
	"mediad/internal/stream"
	"mediad/internal/wav"
)

// Config controls a demo run.
type Config struct {
	OutputDir          string
	Chunks             int
	FramesPerChunk     int
	QueueCapacity      int
	SlowConsumerDelay  time.Duration
	CheckpointEvery    int
	CorruptChunk       int // -1 disables corruption
	TruncateIndex      bool
	Channels           uint16
	SampleRate         uint32
	BitsPerSample      uint16
	PacketsPerChunk    int
	MaxReorder         uint16
}

// Metrics collects the numbers the demo command prints.
type Metrics struct {
	TotalChunks        int
	RoundTripOK        int
	KeyframeHits       int
	HashMatches        int
	Segments           int
	PeakQueueChunks    int
	RecoveredChunks    int
	DuplicatePackets   int
	GapPackets         int
	ResumeAtChunk      int
	LastGoodSegment    int
	FromSnapshot       int
}

// Engine holds the constructed subsystems for one run.
type Engine struct {
	cfg   Config
	plan  *maptrack.Plan
	pipe  *stream.Pipe[*WorkChunk]
	rb    *rtp.ReorderBuffer
	ix    *index.Index
	dec   *wav.Decoder
}

// WorkChunk is a decoded chunk moving through the bounded queue.
type WorkChunk struct {
	ChunkID int
	Frames  [][]byte // per-frame interleaved PCM bytes
	Range   clock.FrameRange
}

// NewEngine validates configuration and prepares the output directory.
func NewEngine(cfg Config) (*Engine, error) {
	if cfg.Chunks < 1 || cfg.FramesPerChunk < 1 || cfg.QueueCapacity < 1 {
		return nil, fault.New(fault.CatHeader, "pipeline.config", true,
			"chunks, frames-per-chunk and queue must be positive")
	}
	if cfg.CheckpointEvery < 1 {
		cfg.CheckpointEvery = cfg.Chunks
	}
	root, err := safeio.EnsureRoot(cfg.OutputDir)
	if err != nil {
		return nil, err
	}
	cfg.OutputDir = root
	layout := maptrack.Layout{
		Channels:      cfg.Channels,
		SampleRate:    cfg.SampleRate,
		BitsPerSample: cfg.BitsPerSample,
		ChannelOrder:  channelNames(cfg.Channels),
	}
	plan, err := maptrack.DefaultMonoPlan(layout)
	if err != nil {
		return nil, err
	}
	return &Engine{
		cfg:  cfg,
		plan: plan,
		pipe: stream.NewPipe[*WorkChunk](cfg.QueueCapacity),
		rb:   rtp.NewReorderBuffer(cfg.SampleRate, 1, cfg.MaxReorder),
	}, nil
}

func channelNames(ch uint16) []string {
	names := []string{"L", "R", "C", "LFE", "BL", "BR", "SL", "SR"}
	out := make([]string, ch)
	for i := range out {
		out[i] = names[i]
	}
	return out
}

// Resume consults the recovery layer and seeds the journal from any valid
// checkpoint so already-verified segments are skipped.
func (e *Engine) Resume() (*recover.Plan, error) {
	plan, err := recover.ValidateAndPlan(e.cfg.OutputDir)
	if err != nil {
		return nil, err
	}
	if plan.LastGoodSegment >= 0 {
		ix, err := index.Load(e.cfg.OutputDir)
		if err != nil {
			return nil, err
		}
		e.ix = ix
	} else {
		e.ix = index.New(e.cfg.SampleRate)
	}
	return plan, nil
}

// FeedWAV feeds one transport chunk's reassembled PCM bytes through the single
// streaming WAV decoder. The RIFF header is supplied on the first chunk (with
// headerOnly set) and every subsequent call delivers raw PCM payload.
func (e *Engine) FeedWAV(ctx context.Context, chunkID int, wavStream []byte) error {
	if e.dec == nil {
		e.dec = wav.NewDecoder(newBytesReader(wavStream))
	} else {
		e.dec.Append(newBytesReader(wavStream))
	}
	frames := make([][]byte, 0, e.cfg.FramesPerChunk)
	for {
		frame, err := e.dec.ReadFrame()
		if err == io.EOF || errors.Is(err, wav.ErrNeedsData) {
			break // drained for now; more chunks may be appended
		}
		if err != nil {
			return err
		}
		frames = append(frames, frame)
	}
	start := uint64(chunkID * e.cfg.FramesPerChunk)
	wc := &WorkChunk{
		ChunkID: chunkID,
		Frames:  frames,
		Range:   clock.FrameRange{Start: start, End: start + uint64(len(frames))},
	}
	return e.pipe.Push(ctx, wc)
}

// IngestRTP parses one RTP datagram into the reorder buffer. Gaps are counted
// and surfaced as recoverable faults so the affected segment is marked.
func (e *Engine) IngestRTP(wire []byte, streamOffset int64) error {
	p, err := rtp.Parse(wire, streamOffset)
	if err != nil {
		return err
	}
	if err := e.rb.Push(p); err != nil {
		return err
	}
	return nil
}

// FeedSkippedWAV pushes bytes for a chunk already covered by a checkpoint.
// The decoder stays contiguous, but the frames are drained without entering
// the bounded queue so verified segments are not rebuilt or double-counted.
func (e *Engine) FeedSkippedWAV(wavStream []byte) error {
	if e.dec == nil {
		e.dec = wav.NewDecoder(newBytesReader(wavStream))
	} else {
		e.dec.Append(newBytesReader(wavStream))
	}
	for {
		if _, err := e.dec.ReadFrame(); err != nil {
			if errors.Is(err, wav.ErrNeedsData) || err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// CloseInput signals that no more chunks will be enqueued.
func (e *Engine) CloseInput() { e.pipe.Close() }

// FinalizeWAV checks that the streaming decoder reached the declared end of
// the data chunk; an unmet expectation becomes a real truncation fault.
func (e *Engine) FinalizeWAV() error {
	if e.dec == nil {
		return nil
	}
	return e.dec.Finalize()
}

// Abort fails the queue so every blocked stage is released.
func (e *Engine) Abort(err error) { e.pipe.CloseWithError(err) }

// Process drains the queue, muxes one segment per chunk and commits each via
// the checkpoint journal. It returns the accumulated metrics.
func (e *Engine) Process(ctx context.Context) (*Metrics, error) {
	m := &Metrics{TotalChunks: e.cfg.Chunks}
	segmentsDir := filepath.Join(e.cfg.OutputDir, "segments")
	if err := os.MkdirAll(segmentsDir, 0o755); err != nil {
		return nil, fault.Wrap(fault.CatWrite, "pipeline.mdir", -1, -1, true, err)
	}
	for {
		wc, err := e.pipe.Pop(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return m, err
		}
		if e.cfg.SlowConsumerDelay > 0 {
			select {
			case <-time.After(e.cfg.SlowConsumerDelay):
			case <-ctx.Done():
				return m, fault.Wrap(fault.CatCanceled, "pipeline.slow", -1, -1, true, ctx.Err())
			}
		}
		if err := e.processChunk(ctx, wc, segmentsDir, m); err != nil {
			return m, err
		}
		if peak := e.pipe.Snapshot().PeakQueued; peak > m.PeakQueueChunks {
			m.PeakQueueChunks = peak
		}
	}
	m.DuplicatePackets = e.rb.DuplicateCount()
	m.GapPackets = e.rb.GapCount()
	m.LastGoodSegment = e.ix.LastGoodID
	return m, nil
}
