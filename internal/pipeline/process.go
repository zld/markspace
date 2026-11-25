package pipeline

import (
	"bytes"
	"context"
	"os"

	"mediad/internal/fault"
	"mediad/internal/index"
	"mediad/internal/mp4"
	"mediad/internal/safeio"
)

// processChunk deinterleaves one chunk's frames into mono tracks, muxes an MP4
// segment, re-parses it, verifies every sample byte and only then appends a
// checkpoint record. An unverified segment never advances the journal.
func (e *Engine) processChunk(ctx context.Context, wc *WorkChunk, segmentsDir string, m *Metrics) error {
	select {
	case <-ctx.Done():
		return fault.Wrap(fault.CatCanceled, "pipeline.chunk", -1, -1, true, ctx.Err())
	default:
	}
	// Idempotent resume: a segment already recorded in a verified checkpoint
	// is skipped rather than remuxed or double-counted.
	if e.isCommitted(wc.ChunkID) {
		if peak := e.pipe.Snapshot().PeakQueued; peak > m.PeakQueueChunks {
			m.PeakQueueChunks = peak
		}
		return nil
	}
	trackCount := e.plan.TrackCount()
	trackPCM := make([][]byte, trackCount)
	for i := range trackPCM {
		trackPCM[i] = make([]byte, 0, len(wc.Frames)*e.plan.Source.SampleSize())
	}
	// Deinterleave every frame; the split rejects partial frames outright.
	for _, frame := range wc.Frames {
		samples, err := e.plan.SplitFrame(frame)
		if err != nil {
			return err
		}
		for tr, sample := range samples {
			trackPCM[tr] = append(trackPCM[tr], sample...)
		}
	}

	specs := make([]mp4.TrackSpec, trackCount)
	for tr, mapping := range e.plan.Tracks {
		specs[tr] = mp4.TrackSpec{
			TrackID:       uint32(mapping.TrackID + 1),
			BitsPerSample: e.plan.Source.BitsPerSample,
			SampleRate:    e.plan.Source.SampleRate,
		}
	}
	seg := mp4.Segment{
		Frames:     wc.Range,
		Tracks:     specs,
		PCM:        trackPCM,
		KeyframeAt: wc.Range.Start,
	}

	fileName := "segments/seg-" + pad6(wc.ChunkID) + ".mp4"
	dst, err := safeio.SafeJoin(e.cfg.OutputDir, fileName)
	if err != nil {
		return err
	}
	// Write to a temp file first; only an atomically renamed file is visible.
	tmp, err := os.CreateTemp(segmentsDir, ".tmp-seg-*")
	if err != nil {
		return fault.Wrap(fault.CatWrite, "pipeline.tmp", -1, -1, true, err)
	}
	tmpName := tmp.Name()
	if err := mp4.MuxWrite(tmp, seg); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fault.Wrap(fault.CatWrite, "pipeline.sync", -1, -1, true, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fault.Wrap(fault.CatWrite, "pipeline.close", -1, -1, true, err)
	}

	// Re-parse the temp file before promotion so a corrupt output never lands.
	rf, err := os.Open(tmpName)
	if err != nil {
		_ = os.Remove(tmpName)
		return fault.Wrap(fault.CatWrite, "pipeline.reopen", -1, -1, true, err)
	}
	parsed, err := mp4.ReadSegment(rf, seg.Frames)
	_ = rf.Close()
	if err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := mp4.Verify(seg, parsed); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	wantHash := mp4.Hash(seg)
	if mp4.HashResult(parsed) != wantHash {
		_ = os.Remove(tmpName)
		return fault.New(fault.CatHash, "pipeline.hash", true,
			"re-parsed segment digest mismatch")
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return fault.Wrap(fault.CatWrite, "pipeline.rename", -1, -1, true, err)
	}

	// Media is durable: now commit the index record and persist the journal.
	rec := index.Record{
		SegmentID:  wc.ChunkID,
		ChunkStart: wc.ChunkID,
		ChunkEnd:   wc.ChunkID + 1,
		FrameRange: wc.Range,
		FileName:   fileName,
		Keyframes:  []uint64{wc.Range.Start},
		Hash:       wantHash,
		Complete:   true,
		Tracks:     make([]index.TrackIndex, trackCount),
	}
	for tr, mapping := range e.plan.Tracks {
		rec.Tracks[tr] = index.TrackIndex{
			TrackID:       mapping.TrackID,
			SourceChannel: mapping.SourceChannel,
			Bytes:         len(trackPCM[tr]),
		}
	}
	e.ix.Append(rec)
	if err := e.ix.Save(e.cfg.OutputDir); err != nil {
		return err
	}
	if e.cfg.CheckpointEvery > 0 && (wc.ChunkID+1)%e.cfg.CheckpointEvery == 0 {
		snapSeq := (wc.ChunkID + 1) / e.cfg.CheckpointEvery
		if err := e.ix.SaveSnapshot(e.cfg.OutputDir, snapSeq); err != nil {
			return err
		}
	}

	m.Segments++
	m.RoundTripOK++
	m.KeyframeHits++
	m.HashMatches++
	return nil
}

// isCommitted reports whether a chunk already has a complete checkpoint record.
func (e *Engine) isCommitted(chunkID int) bool {
	if e.ix == nil || chunkID >= len(e.ix.Records) {
		return false
	}
	return e.ix.Records[chunkID].Complete
}

// pad6 renders an id as a zero-padded six-character file name component.
func pad6(id int) string {
	const digits = "0123456789"
	out := make([]byte, 6)
	for i := 5; i >= 0; i-- {
		out[i] = digits[id%10]
		id /= 10
	}
	return string(out)
}

// bytesReader is the local io.Reader used to feed chunk payloads.
type bytesReader struct{ data *bytes.Reader }

func newBytesReader(b []byte) *bytesReader {
	return &bytesReader{data: bytes.NewReader(b)}
}

func (r *bytesReader) Read(p []byte) (int, error) { return r.data.Read(p) }
