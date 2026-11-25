package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash/crc32"
	"sync"
	"time"

	"github.com/zld/media-regress/audio"
	"github.com/zld/media-regress/cache"
	"github.com/zld/media-regress/checkpoint"
	"github.com/zld/media-regress/internal/clock"
	"github.com/zld/media-regress/internal/error"
	"github.com/zld/media-regress/manifest"
	"github.com/zld/media-regress/media"
	"github.com/zld/media-regress/stream"
)

// runState carries the per-run mutable state across stages.
type runState struct {
	opts        Options
	res         *Result
	asset       *media.Asset
	track       *media.Track
	inputDigest string
	total       int

	conv     *audio.Converter
	pipeline *stream.Pipeline
	cp       *checkpoint.Store
	store    *cache.Store
	// startBlock is the first block to process on this run.
	startBlock int
	// interruptAt stops the initial run after this block; it is zero on a
	// resumed run so progress is always possible.
	interruptAt int
}

// openCheckpoint loads or creates the checkpoint and resolves the resume
// position. It also opens the content cache when configured.
func (r *runState) openCheckpoint() (int, error) {
	if r.opts.CacheDir != "" {
		s, err := cache.Open(r.opts.CacheDir)
		if err != nil {
			return 0, err
		}
		r.store = s
	}
	if r.opts.CheckpointPath == "" && !r.opts.Resume {
		return 0, nil
	}
	if r.opts.CheckpointPath == "" {
		return 0, merr.New(merr.StageCheckpt, merr.KindParse, 0, 0, "--resume requires --checkpoint")
	}
	fresh := checkpoint.NewFile(runID(r.asset), r.opts.Source, r.inputDigest, r.total)
	cp, err := checkpoint.Open(r.opts.CheckpointPath, fresh)
	if err != nil {
		return 0, err
	}
	r.cp = cp
	if !r.opts.Resume {
		// A fresh run overwrites any previous progress for this path.
		if err := cp.Save(); err != nil {
			return 0, err
		}
		return 0, nil
	}
	plan, err := checkpoint.Plan(cp.File(), r.inputDigest)
	if err != nil {
		return 0, err
	}
	r.res.Resume = plan
	// resume_block reports the last committed block, not the next to process.
	r.res.Summary.ResumeBlock = plan.StartBlock - 1
	r.res.Summary.RecomputedBlocks = cp.File().RecomputedBlocks(plan.StartBlock)
	return plan.StartBlock, nil
}

func runID(a *media.Asset) string {
	h := sha256.New()
	h.Write([]byte(a.Source))
	h.Write([]byte{0})
	h.Write([]byte(a.Digest))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// buildManifest seeds the manifest from the parsed asset and target audio.
func (r *runState) buildManifest() (*manifest.Builder, error) {
	target := manifest.AudioTarget{
		SampleRate:   r.opts.SampleRate,
		Layout:       r.opts.Layout,
		Channels:     uint16(len(r.conv.TargetLayout.Channels)),
		LoudnessLUFS: formatLoudness(r.opts.Loudness),
		GainDB:       formatGain(r.conv.GainDB),
	}
	b := manifest.NewBuilder(r.opts.Source, manifest.Format(r.asset.Format), r.inputDigest)
	b.SetAudioTarget(target)
	b.DropSegment(r.opts.DropSegment)
	for _, t := range r.asset.Tracks {
		if err := b.AddTrack(t); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// cacheKey builds the content key for one block.
func (r *runState) cacheKey(block int) cache.Key {
	return cache.Key{
		Source: r.opts.Source, Input: r.inputDigest, Block: block,
		Params: cache.Params{
			SampleRate:   r.opts.SampleRate,
			Layout:       r.opts.Layout,
			LoudnessLUFS: formatLoudness(r.opts.Loudness),
			GainDB:       formatGain(r.conv.GainDB),
		}.Key(),
	}
}

// processBlocks converts every block from startBlock onward, routing each
// through the bounded queue and the cache, and records commits. The consumer
// runs in its own goroutine so a slow consumer exerts real backpressure on the
// producer and the queue never exceeds its configured bounds.
func (r *runState) processBlocks(b *manifest.Builder) error {
	ctx := context.Background()
	sourceRate := r.track.Audio.SampleRate
	convStream := r.conv.Start(r.track.Duration)

	// A resumed run replays committed blocks into the manifest and advances
	// the converter's resampler phase over them, so later ticks line up
	// exactly with an uninterrupted run.
	sourceStart := int64(0)
	if r.cp != nil && r.startBlock > 0 {
		for _, c := range r.cp.File().CommitsThrough(r.startBlock - 1) {
			b.AddSegment(manifest.Segment{
				Index: c.Block, TrackID: c.TrackID, StartTick: c.StartTick, EndTick: c.EndTick,
				Frames: c.Frames, Digest: c.OutputDigest, Bytes: c.Bytes,
			})
		}
	}
	// Blocks are 1-based on the command line and in the checkpoint, so
	// startBlock-1 blocks are already committed and must be replayed for phase.
	prefix := r.startBlock - 1
	if prefix < 0 {
		prefix = 0
	}
	for i := 0; i < prefix && i < len(r.track.Samples); i++ {
		n := int64(sourceFrameCount(r.track.Samples[i].Data, r.conv))
		if _, err := convStream.ConvertBlock(r.track.Samples[i].Data, sourceStart, i, r.track.Samples[i].Offset); err != nil {
			return err
		}
		sourceStart += n
	}

	var consumerErr error
	var errMu sync.Mutex
	consumer := func(ctx context.Context, d stream.Delivery) error {
		if err := r.commitDelivery(b, d); err != nil {
			errMu.Lock()
			consumerErr = err
			errMu.Unlock()
			return err
		}
		return nil
	}
	p, err := stream.NewPipeline(stream.Config{MaxBlocks: r.pipeline.MaxBlocks(), MaxBytes: r.pipeline.MaxBytes()}, consumer)
	if err != nil {
		return err
	}
	p.ConsumerDelay = time.Duration(r.opts.SlowConsumerMs) * time.Millisecond
	pz := stream.NewPacketizer(p)
	runDone := make(chan error, 1)
	go func() { runDone <- p.Run(ctx) }()

	var pushErr error
	pushOrdinal := 0
	for i := prefix; i < len(r.track.Samples); i++ {
		sample := r.track.Samples[i]
		blockIndex := i + 1
		if sample.HasCRC {
			if got := crc32.ChecksumIEEE(sample.Data); got != sample.CRC32 {
				pushErr = merr.New(merr.StageStream, merr.KindCorrupt, r.track.ID, sample.Offset,
					"audio block %d checksum %08x does not match recorded %08x", blockIndex, got, sample.CRC32)
				break
			}
		}
		block, err := convStream.ConvertBlock(sample.Data, sourceStart, i, sample.Offset)
		if err != nil {
			pushErr = err
			break
		}
		digest := manifest.SegmentDigest(block.PCM)

		if r.store != nil {
			key := r.cacheKey(blockIndex)
			if r.opts.TamperCache && blockIndex == 2 && r.store.Lookup(key) {
				if err := r.store.Tamper(key); err != nil {
					pushErr = err
					break
				}
			}
			e, ok, err := r.store.Get(key)
			if err != nil {
				pushErr = err
				break
			}
			if ok {
				digest = e.OutputDigest
				block.PCM = e.Payload
				block.Frames = len(e.Payload) / (len(r.conv.TargetLayout.Channels) * 2)
			} else if _, err := r.store.Put(key, block.PCM); err != nil {
				pushErr = err
				break
			}
		}

		// The RTP timestamp is expressed in the target sample rate, matching
		// the published timeline rather than the source rate.
		tsTick, err := clock.ConvertTicks(sourceStart, clock.Rate(sourceRate), clock.Rate(r.opts.SampleRate))
		if err != nil {
			pushErr = err
			break
		}
		item := stream.Item{
			Index: blockIndex, TrackID: r.track.ID, Timestamp: uint32(tsTick),
			Payload: block.PCM, SourceFrames: block.SourceFrames,
			InputOffset: sample.Offset,
			// Sequence is the wire order of this run's pushes, which is exactly
			// what the pipeline assigns, so packet and queue record agree even
			// when a resume skips earlier blocks.
			Sequence: uint16(pushOrdinal),
		}
		item.Digest = digest
		item.Channels = uint16(len(r.conv.TargetLayout.Channels))
		last := i == len(r.track.Samples)-1
		pushOrdinal++
		if err := pz.Push(ctx, item, last); err != nil {
			// The consumer may have stopped the run; surface its error.
			pushErr = err
			break
		}
		sourceStart += int64(sourceFrameCount(sample.Data, r.conv))
	}

	p.Close()
	runErr := <-runDone
	errMu.Lock()
	cErr := consumerErr
	errMu.Unlock()
	if cErr != nil {
		return cErr
	}
	if pushErr != nil {
		return pushErr
	}
	if runErr != nil && runErr != stream.ErrDrained {
		return runErr
	}

	stats := p.Stats()
	r.res.Summary.QueuePeakBytes = stats.QueuePeakBytes
	r.res.Summary.QueuePeakBlocks = stats.QueuePeakBlocks
	r.res.PacketStats = pz.Stats()
	if convStream.Truncated() {
		r.res.Summary.TruncatedBlocks = 1
	}
	if r.store != nil {
		r.res.Summary.CacheRejected = r.store.Stats().Rejected
	}
	return nil
}
