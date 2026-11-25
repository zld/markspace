package pipeline

import (
	"github.com/zld/media-regress/audio"
	"github.com/zld/media-regress/checkpoint"
	"github.com/zld/media-regress/internal/clock"
	"github.com/zld/media-regress/internal/error"
	"github.com/zld/media-regress/manifest"
	"github.com/zld/media-regress/stream"
)

// commitDelivery records one delivered block in the manifest and checkpoint.
func (r *runState) commitDelivery(b *manifest.Builder, d stream.Delivery) error {
	// The delivered payload is an RTP packet. Decode it so the consumer commits
	// exactly the essence the packet carries, and re-validate sequence and
	// timestamp instead of trusting the producer.
	seq, ts, essence, err := stream.DecodePacket(d.Payload, d.TrackID, r.opts.SampleRate)
	if err != nil {
		return err
	}
	if seq != d.Sequence || ts != d.Timestamp {
		return merr.New(merr.StageStream, merr.KindCorrupt, d.TrackID, int64(d.Index),
			"rtp packet for block %d disagrees with its queue record", d.Index)
	}
	frames := len(essence) / (int(d.Channels) * 2)
	if frames == 0 {
		return merr.New(merr.StageStream, merr.KindInconsistent, d.TrackID, int64(d.Index),
			"delivered block %d is empty", d.Index)
	}
	startTick, err := clock.ConvertTicks(int64(d.Timestamp), clock.Rate(r.opts.SampleRate), clock.Master)
	if err != nil {
		return err
	}
	block := audio.Block{Index: d.Index, Frames: frames, PCM: essence}
	seg, err := manifest.SegmentFromBlock(d.TrackID, d.Index, block, startTick, r.opts.SampleRate, d.Digest)
	if err != nil {
		return err
	}
	b.AddSegment(seg)
	if r.cp != nil {
		state := checkpoint.StageState{
			Stage: checkpoint.StageAudio, InputDigest: r.inputDigest,
			CommittedBlock: d.Index, Timestamp: d.Timestamp, OutputDigest: d.Digest,
		}
		r.cp.Record(state)
		state.Stage = checkpoint.StageStream
		r.cp.Record(state)
		r.cp.File().AppendCommit(checkpoint.Commit{
			Block: d.Index, TrackID: d.TrackID, Timestamp: d.Timestamp,
			StartTick: seg.StartTick, EndTick: seg.EndTick, Frames: seg.Frames,
			Bytes: seg.Bytes, OutputDigest: seg.Digest,
		})
		if d.Index == 1 {
			r.cp.Record(checkpoint.StageState{Stage: checkpoint.StageProbe, InputDigest: r.inputDigest, CommittedBlock: -1})
			r.cp.Record(checkpoint.StageState{Stage: checkpoint.StageMetadata, InputDigest: r.inputDigest, CommittedBlock: -1})
		}
		if err := r.cp.Save(); err != nil {
			return err
		}
	}
	// An interrupted first run records where it stopped; a resumed run is not
	// interrupted again, otherwise it could never make progress.
	if r.interruptAt != 0 && d.Index >= r.interruptAt {
		return merr.New(merr.StageStream, merr.KindCanceled, d.TrackID, d.InputOffset,
			"interrupted after committing block %d; checkpoint saved", d.Index)
	}
	return nil
}

// sourceFrameCount returns the number of complete frames in a source block.
func sourceFrameCount(data []byte, conv *audio.Converter) int {
	frameBytes := len(conv.SourceLayout.Channels) * 2
	if frameBytes == 0 {
		return 0
	}
	return len(data) / frameBytes
}
