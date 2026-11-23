package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"mediamux/analyze"
	"mediamux/cache"
	"mediamux/media"
	"mediamux/stream"
)

// processSegment is the CAS/analysis decision for a single segment.
func (r *Runner) processSegment(ctx context.Context, in *Input, i int) (SegmentOutcome, []Event, error) {
	seg := in.Manifest.Segments[i]
	node := in.Nodes[i]
	data, err := r.segmentBytes(ctx, in, i)
	if err != nil {
		return SegmentOutcome{}, nil, err
	}
	digest := analyze.Digest(data)
	prov := cache.Provenance{
		Digest: digest, Node: node, SegmentID: seg.ID, SegmentIdx: i,
		InputLength: int64(len(data)), ManifestVer: analyze.Digest([]byte(in.ManifestRaw)),
		Epoch: r.opts.Epoch,
	}
	expect := prov
	if r.opts.FakeSourceNode != "" {
		expect.Node = r.opts.FakeSourceNode
	}
	var rtpRes *analyze.RTPResult
	if i < len(in.RTP) && len(in.RTP[i]) > 0 {
		// Transport evidence is cheap local work and is not part of the
		// content-addressed container record, so it is always re-derived.
		rtpRes = analyze.AnalyzeRTP(in.RTP[i], analyze.RTPOptions{ClockRate: 90000})
	}
	var events []Event
	lookup := r.cache.Lookup(digest, expect)
	if lookup.Hit {
		var out SegmentOutcome
		if err := json.Unmarshal(lookup.Entry.Meta, &out); err == nil && out.Digest == digest {
			out.FromCache = true
			out.Node = node
			applyRTP(&out, rtpRes)
			events = append(events, Event{Kind: "cache_hit", Segment: i,
				Detail: "source node, input length and sha256 verified"})
			return out, events, nil
		}
		// A verified entry whose analysis record cannot be decoded is treated
		// as poisoned rather than trusted.
		events = append(events, Event{Kind: "cache_reject", Segment: i,
			Detail: "verified body had an unreadable analysis record; rebuilt"})
	} else if lookup.Rejection != nil {
		events = append(events, Event{Kind: "cache_source_reject", Segment: i, Detail: lookup.Reason})
	}

	if err := ctx.Err(); err != nil {
		return SegmentOutcome{}, nil, media.NewError(media.CodeCanceled, media.PhasePipeline,
			"analysis canceled").AtOffset(int64(i)).Because(err)
	}
	segRes := analyze.AnalyzeMP4(i, seg.ID, node, data, 0, analyze.MP4Options{})
	out := SegmentOutcome{
		Index: i, ID: seg.ID, Node: node, Digest: digest, Bytes: int64(len(data)),
		Corrupt: segRes.Corrupt, Damaged: segRes.Damaged, Recovery: segRes.Recovery,
		Quality:      analyze.SegmentSample(segRes),
		KeyframeGaps: segRes.KeyframeGaps,
	}
	applyRTP(&out, rtpRes)
	if out.Corrupt {
		for _, d := range out.Damaged {
			events = append(events, Event{Kind: "corrupt_region", Segment: i,
				Detail: fmt.Sprintf("offset=%d length=%d code=%s reason=%s", d.Offset, d.Length, d.Code, d.Reason)})
		}
	}
	meta, err := json.Marshal(out)
	if err != nil {
		return SegmentOutcome{}, nil, media.NewError(media.CodeInternal, media.PhasePipeline,
			"encode analysis record").AtOffset(int64(i)).Because(err)
	}
	prov.MetaDigest = analyze.Digest(meta)
	// Store the analysis record next to the body so a later hit can skip
	// analysis; the meta digest is part of the verification contract.
	if _, err := r.cache.PutWithMeta(data, prov, meta); err != nil {
		return SegmentOutcome{}, nil, err
	}
	return out, events, nil
}

// segmentBytes returns the segment's bytes. Inline payloads are used directly;
// streamed sources are read through the bounded ChunkReader under ctx so the
// memory ceiling and cancellation apply to the actual read path too.
func (r *Runner) segmentBytes(ctx context.Context, in *Input, i int) ([]byte, error) {
	if in.Segments != nil {
		return in.Segments[i], nil
	}
	if i >= len(in.Sources) || in.Sources[i] == nil {
		return nil, media.NewError(media.CodeInput, media.PhaseRead,
			fmt.Sprintf("segment %d has no payload or source", i)).AtOffset(int64(i))
	}
	declared := int64(0)
	if i < len(in.SourceSizes) {
		declared = in.SourceSizes[i]
	}
	if declared <= 0 || declared > r.opts.MemoryLimit {
		return nil, media.NewError(media.CodeInput, media.PhaseRead,
			fmt.Sprintf("segment %d size %d outside the memory limit", i, declared)).
			AtOffset(int64(i)).OnSegment(i, in.Manifest.Segments[i].ID)
	}
	cr, err := stream.NewChunkReader(ctx, in.Sources[i], declared)
	if err != nil {
		return nil, err
	}
	defer cr.Close()
	buf := make([]byte, 0, declared)
	for {
		chunk, err := cr.NextChunk()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if int64(len(buf)+len(chunk)) > declared {
			return nil, media.NewError(media.CodeInput, media.PhaseRead,
				"segment source exceeds its declared length").AtOffset(cr.BytesRead()).
				OnSegment(i, in.Manifest.Segments[i].ID)
		}
		buf = append(buf, chunk...)
	}
	if int64(len(buf)) != declared {
		return nil, media.NewError(media.CodeTruncated, media.PhaseRead,
			fmt.Sprintf("segment %d ended after %d of %d bytes", i, len(buf), declared)).
			AtOffset(cr.BytesRead()).OnSegment(i, in.Manifest.Segments[i].ID)
	}
	return buf, nil
}

// applyRTP folds transport evidence into an outcome. A transport fault is
// distinct from container damage: the fragment structure is intact, but media
// time is missing.
func applyRTP(out *SegmentOutcome, rtpRes *analyze.RTPResult) {
	if rtpRes == nil {
		return
	}
	out.RTP = rtpRes
	if rtpRes.Corrupt {
		out.Corrupt = true
		if out.Quality.CorruptMS == 0 {
			out.Quality.CorruptMS = out.Quality.DurationMS
		}
		if out.Recovery == "" {
			out.Recovery = "transport fault located; container structure intact"
		}
	}
}
