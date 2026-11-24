package pipeline

import (
	"context"
	"fmt"
	"time"

	"medpipe/internal/adapter/mp4v1"
	"medpipe/internal/clock"
	"medpipe/internal/ingest"
	"medpipe/internal/media"
	"medpipe/internal/sample"
)

func (r *Runner) recordParseError(e *clock.Error) {
	if e == nil {
		return
	}
	r.state.Stats.ParseErrors++
	if e.Code == clock.CodeCorruptPayload {
		r.state.Stats.CorruptPackets++
	}
	if r.state.FirstError == nil {
		r.state.FirstError = e
		r.state.ErrorCode = e.Code
	}
}

func (r *Runner) processChunk(ch sample.Chunk) error {
	switch ch.Kind {
	case sample.ChunkRTP:
		item := r.rtp.Parse(ch.Data, ch.Offset, r.tl, time.Now().UTC())
		r.state.Stats.RTPPackets++
		if item.Err != nil {
			r.recordParseError(item.Err)
		}
		for _, ordered := range r.reorder.Submit(item) {
			if err := r.handleOrdered(ordered); err != nil {
				return err
			}
		}

	case sample.ChunkRTCP:
		r.state.Stats.RTCPPackets++
		item := r.rtcp.Parse(ch.Data, ch.Offset, r.tl, time.Now().UTC())
		if item.Err != nil {
			// Clock rollback is located and retained, but does not stop media.
			r.recordParseError(item.Err)
		}

	default:
		r.recordParseError(clock.Errorf(clock.CodeInvalidPacket,
			&clock.Location{ByteOffset: ch.Offset}, "unknown source chunk kind %d", ch.Kind))
	}
	return nil
}

func (r *Runner) handleOrdered(item ingest.Item) error {
	r.state.Stats.DeliveredItems++
	r.state.LastSequence = item.Sequence
	r.state.LastExtSeq = item.ExtSeq
	if item.Err != nil {
		// Parse error was already classified at parse time; the ordered item is
		// skipped so invalid/truncated media never reaches segmentation.
		return nil
	}
	if item.Frame == nil {
		return nil
	}
	r.state.Stats.ValidFrames++
	built, err := r.segmenter.Accept(*item.Frame)
	if err != nil {
		return err
	}
	if built != nil {
		if err := r.finalizeSegment(built); err != nil {
			return err
		}
	}
	if sr := r.silence.Add(*item.Frame); sr != nil {
		r.state.SilenceRanges = append(r.state.SilenceRanges, *sr)
	}
	if built != nil {
		// Segment bytes are durable and silence state for this boundary frame
		// has also been advanced; this is a complete cross-subsystem point.
		if err := r.saveCheckpoint(false, ""); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) finalizeSegment(built *media.BuiltSegment) error {
	seg := built.Info
	if r.state.Segments == nil {
		r.state.Segments = []media.Segment{}
	}

	if dupOf := r.deduper.Observe(seg.Fingerprint, seg.Index); dupOf >= 0 {
		seg.DuplicateOf = dupOf
		for _, first := range r.state.Segments {
			if first.Index == dupOf {
				seg.File = first.File
				seg.SHA256 = first.SHA256
				seg.ByteSize = first.ByteSize
				break
			}
		}
		r.state.Segments = append(r.state.Segments, seg)
		return nil
	}

	payload, err := encodeSegmentPayload(built)
	if err != nil {
		return fmt.Errorf("encode segment %d payload: %w", seg.Index, err)
	}
	mp4Bytes, err := mp4v1.EncodeSegment(payload)
	if err != nil {
		return fmt.Errorf("mux segment %d to MP4: %w", seg.Index, err)
	}
	if err := verifySegmentMP4(mp4Bytes, seg.Index); err != nil {
		return fmt.Errorf("verify segment %d MP4: %w", seg.Index, err)
	}

	rel := fmt.Sprintf("segments/segment-%04d.mp4", seg.Index)
	abs := r.opts.OutDir + "/" + rel
	if err := clock.AtomicWriteFile(abs, mp4Bytes, 0o644); err != nil {
		return fmt.Errorf("write segment %d: %w", seg.Index, err)
	}
	seg.File = rel
	seg.SHA256 = sha256Hex(mp4Bytes)
	seg.ByteSize = len(mp4Bytes)
	r.state.Segments = append(r.state.Segments, seg)
	return nil
}

// flushReorderAndSegments is called after EOF.
func (r *Runner) flushReorderAndSegments(ctx context.Context) error {
	for _, item := range r.reorder.Flush() {
		if err := r.handleOrdered(item); err != nil {
			return err
		}
	}
	if sr := r.silence.Flush(); sr != nil {
		r.state.SilenceRanges = append(r.state.SilenceRanges, *sr)
	}
	if built := r.segmenter.Flush(); built != nil {
		if err := r.finalizeSegment(built); err != nil {
			return err
		}
	}
	for _, iss := range r.reorder.Issues {
		r.tl.NoteIssue(iss)
	}
	if !r.tl.HasSenderReport(r.cfg.SSRC) {
		_ = r.tl.RequireSenderReport(r.cfg.SSRC, r.state.LastSequence, r.state.NextSourceOffset)
	}
	return nil
}
