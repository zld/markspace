package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pion/rtp"

	"mediad/internal/fault"
	"mediad/internal/pipeline"
	"mediad/internal/recover"
	"mediad/internal/wav"
)

func runDemo(args []string) int {
	f, err := parseFlags(args)
	if err != nil {
		return exitInternal
	}
	outDir := f.out
	if outDir == "" {
		outDir, err = os.MkdirTemp("", "mediad-demo-")
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot create output dir: %v\n", err)
			return exitInternal
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := pipeline.Config{
		OutputDir:         outDir,
		Chunks:            f.chunks,
		FramesPerChunk:    f.framesPerChunk,
		QueueCapacity:     f.queue,
		SlowConsumerDelay: time.Duration(f.slowConsumerMs) * time.Millisecond,
		CheckpointEvery:   f.checkpointEvery,
		CorruptChunk:      f.corruptChunk,
		TruncateIndex:     f.truncateIndex,
		Channels:          uint16(f.channels),
		SampleRate:        uint32(f.sampleRate),
		BitsPerSample:     uint16(f.bits),
		PacketsPerChunk:   f.packetsPerChunk,
		MaxReorder:        uint16(f.maxReorder),
	}

	eng, err := pipeline.NewEngine(cfg)
	if err != nil {
		return reportError(err)
	}
	plan, err := eng.Resume()
	if err != nil {
		return reportError(err)
	}

	// Run the full pipeline and commit every checkpoint.
	metrics, err := runOnce(ctx, eng, f, plan)
	if err != nil {
		return reportError(err)
	}

	if !f.truncateIndex {
		metrics.ResumeAtChunk = plan.ResumeAtChunk
		metrics.FromSnapshot = plan.FromSnapshot
		metrics.RecoveredChunks = plan.RedoSegments
		printMetrics(metrics, exitOK)
		return exitOK
	}

	// Damage the latest commit, then recover and re-run the affected tail of
	// the job from the last valid snapshot.
	if err := eng.SimulateCrash(); err != nil {
		return reportError(err)
	}
	eng.ResetForRerun()
	replan, redo, err := eng.RerunAfterCrash()
	if err != nil {
		return reportError(err)
	}
	recoveredMetrics, err := runOnce(ctx, eng, f, replan)
	if err != nil {
		return reportError(err)
	}
	recoveredMetrics.ResumeAtChunk = replan.ResumeAtChunk
	recoveredMetrics.FromSnapshot = replan.FromSnapshot
	recoveredMetrics.RecoveredChunks = redo
	fmt.Printf("valid_checkpoint_segment=%d\n", replan.LastGoodSegment)
	printMetrics(recoveredMetrics, exitOK)
	return exitOK
}

// runOnce executes one producer/consumer pass against the supplied plan and
// returns the metrics gathered while muxing. The producer's root error always
// takes precedence over the consumer's view of the same failure.
func runOnce(ctx context.Context, eng *pipeline.Engine, f *flags, plan *recover.Plan) (*pipeline.Metrics, error) {
	feedErr := make(chan error, 1)
	go func() {
		if err := feed(ctx, eng, f, plan); err != nil {
			eng.Abort(err)
			feedErr <- err
			return
		}
		if err := eng.FinalizeWAV(); err != nil {
			eng.Abort(err)
			feedErr <- err
			return
		}
		eng.CloseInput()
		feedErr <- nil
	}()

	metrics, procErr := eng.Process(ctx)
	if err := <-feedErr; err != nil {
		return metrics, err
	}
	return metrics, procErr
}

// feed synthesizes the WAV/RTP fixtures and streams each chunk into the
// engine. The synthetic RTP sequence/timestamp counters start near their
// modular maxima so wraparound is exercised on every normal run.
func feed(ctx context.Context, eng *pipeline.Engine, f *flags, plan *recover.Plan) error {
	format := wav.Format{
		AudioFormat:   1,
		NumChannels:   uint16(f.channels),
		SampleRate:    uint32(f.sampleRate),
		BitsPerSample: uint16(f.bits),
	}
	frameSize := format.FrameSize()
	totalFrames := f.chunks * f.framesPerChunk
	pcm := make([]byte, totalFrames*frameSize)
	for i := range pcm {
		pcm[i] = byte(i*13 + 7)
	}
	whole, err := wav.Encode(format, pcm)
	if err != nil {
		return fault.Wrap(fault.CatWrite, "demo.encode", -1, -1, true, err)
	}
	headerLen := len(whole) - len(pcm)

	var seq uint16 = 0xFFF0
	var ts uint32 = 0xFFFFFF00
	streamOffset := int64(0)
	for chunk := 0; chunk < f.chunks; chunk++ {
		start := headerLen + chunk*f.framesPerChunk*frameSize
		end := start + f.framesPerChunk*frameSize
		payload := whole[start:end]

		// Already-verified chunks are not reprocessed. Their PCM still has to
		// flow through the WAV decoder so its byte stream stays contiguous;
		// chunk 0 additionally carries the RIFF header. No RTP is generated
		// and nothing is enqueued for skipped chunks.
		if chunk < plan.ResumeAtChunk {
			var skipped []byte
			if chunk == 0 {
				skipped = append(append([]byte(nil), whole[:headerLen]...), payload...)
			} else {
				skipped = payload
			}
			if err := eng.FeedSkippedWAV(skipped); err != nil {
				return err
			}
			seq += uint16(f.packetsPerChunk)
			ts += uint32(f.framesPerChunk)
			continue
		}

		// Corruption path: drop the final RTP packet of the chosen chunk so
		// the transport sees an unresolvable gap at a reported offset.
		packets := f.packetsPerChunk
		if f.corruptChunk == chunk {
			packets--
			if packets == 0 {
				return fault.New(fault.CatTruncated, "demo.corrupt", true,
					fmt.Sprintf("chunk %d truncated: no packets delivered", chunk))
			}
		}

		chunkBytes := payload
		per := len(chunkBytes) / f.packetsPerChunk
		for p := 0; p < packets; p++ {
			lo := p * per
			hi := lo + per
			if p == f.packetsPerChunk-1 {
				hi = len(chunkBytes)
			}
			pkt := &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    96,
					SequenceNumber: seq,
					Timestamp:      ts,
					SSRC:           0x5151A,
					Marker:         p == f.packetsPerChunk-1,
				},
				Payload: chunkBytes[lo:hi],
			}
			wire, err := pkt.Marshal()
			if err != nil {
				return fault.Wrap(fault.CatWrite, "demo.rtp", streamOffset, -1, true, err)
			}
			if err := eng.IngestRTP(wire, streamOffset); err != nil {
				return err
			}
			streamOffset += int64(len(wire))
			seq++
			ts += uint32(f.framesPerChunk / f.packetsPerChunk)
		}

		// The WAV decoder receives the chunk payload; for the first chunk the
		// RIFF header is prepended so streaming parse works end to end.
		wavChunk := payload
		if chunk == 0 {
			wavChunk = append(append([]byte(nil), whole[:headerLen]...), payload...)
		}
		if f.corruptChunk == chunk {
			// Report the exact byte offset where the truncated chunk begins.
			return fault.Wrap(fault.CatRTPGap, "demo.feed", int64(start), -1,
				true, errGapSynthetic)
		}
		if err := eng.FeedWAV(ctx, chunk, wavChunk); err != nil {
			return err
		}
	}
	return nil
}

var errGapSynthetic = newSyntheticGap()

func newSyntheticGap() error { return &gapErr{} }

type gapErr struct{}

func (*gapErr) Error() string { return "synthetic RTP gap: final packet of chunk dropped" }
