package decode

import (
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"medfix/internal/timeline"
)

// emitSamples materializes samples from the accumulated tables, reading each
// sample's bytes individually from mdat (streaming, no full mdat load).
func (p *mp4Parser) emitSamples(id uint32, ctx *stblContext, track timeline.Track) error {
	n := len(ctx.stsz)
	if n == 0 {
		return nil
	}
	offsets, err := p.sampleOffsets(ctx, n)
	if err != nil {
		return err
	}
	key := map[uint32]bool{}
	for _, s := range ctx.stss {
		key[s] = true
	}
	var ts int64
	sttsIdx, sttsLeft := 0, 0
	if len(ctx.stts) > 0 {
		sttsLeft = int(ctx.stts[0].SampleCount)
	}
	for i := 0; i < n; i++ {
		delta, err := p.nextDelta(ctx, &sttsIdx, &sttsLeft)
		if err != nil {
			return timeline.NewError(timeline.CodeTruncatedBox, "parse", ctx.trackID,
				int64(offsets[i]), "stts shorter than sample count", err)
		}
		ts += delta
		size := int64(ctx.stsz[i])
		begin := int64(offsets[i])
		payload := make([]byte, size)
		if _, err := p.r.Seek(begin, io.SeekStart); err != nil {
			return timeline.NewError(timeline.CodeTruncatedBox, "parse", ctx.trackID, begin, "mdat seek failed", err)
		}
		if _, err := io.ReadFull(p.r, payload); err != nil {
			return timeline.NewError(timeline.CodeTruncatedBox, "parse", ctx.trackID,
				begin, fmt.Sprintf("sample %d truncated: want %d bytes", i+1, size), err)
		}
		p.seq++
		p.sink.OnEvent(timeline.Event{
			Kind: timeline.KindSample, TrackID: ctx.trackID, SourceID: p.sourceID,
			Seq: p.seq, RawTS: ts, Keyframe: key[uint32(i+1)],
			RawPayload: payload, ContentHash: hashBytes(payload),
			Offset: timeline.OffsetRange{Begin: begin, End: begin + size},
		})
	}
	return nil
}

func (p *mp4Parser) nextDelta(ctx *stblContext, idx, left *int) (int64, error) {
	if *idx >= len(ctx.stts) {
		return 0, fmt.Errorf("stts exhausted")
	}
	d := int64(ctx.stts[*idx].SampleDelta)
	*left--
	if *left == 0 {
		*idx++
		if *idx < len(ctx.stts) {
			*left = int(ctx.stts[*idx].SampleCount)
		}
	}
	return d, nil
}

// sampleOffsets expands stsc/stco/co64 into one absolute offset per sample.
func (p *mp4Parser) sampleOffsets(ctx *stblContext, n int) ([]uint64, error) {
	chunks := ctx.stco
	if len(chunks) == 0 {
		chunks = ctx.co64
	}
	if len(chunks) == 0 {
		return nil, timeline.NewError(timeline.CodeTruncatedBox, "parse", ctx.trackID,
			-1, "missing stco/co64 chunk offsets", nil)
	}
	out := make([]uint64, 0, n)
	sc := ctx.stsc
	if len(sc) == 0 {
		return nil, timeline.NewError(timeline.CodeTruncatedBox, "parse", ctx.trackID,
			-1, "missing stsc sample-to-chunk table", nil)
	}
	sampleCursor := 0
	for chunk := 0; chunk < len(chunks); chunk++ {
		perChunk := samplesPerChunk(sc, uint32(chunk+1))
		off := chunks[chunk]
		for j := uint32(0); j < perChunk; j++ {
			if sampleCursor >= n {
				return nil, timeline.NewError(timeline.CodeTruncatedBox, "parse", ctx.trackID,
					int64(off), "more chunk samples than stsz entries", nil)
			}
			out = append(out, off)
			off += uint64(ctx.stsz[sampleCursor])
			sampleCursor++
		}
	}
	if sampleCursor < n {
		return nil, timeline.NewError(timeline.CodeTruncatedBox, "parse", ctx.trackID,
			int64(chunks[len(chunks)-1]), "chunks shorter than sample count", nil)
	}
	return out, nil
}

// samplesPerChunk resolves the active stsc entry for a 1-based chunk number.
func samplesPerChunk(entries []mp4.StscEntry, chunk uint32) uint32 {
	active := entries[0]
	for _, e := range entries {
		if e.FirstChunk <= chunk {
			active = e
		} else {
			break
		}
	}
	return active.SamplesPerChunk
}
