package pipeline

import (
	"context"
	"encoding/json"
	"fmt"

	"mediamux/analyze"
	"mediamux/cache"
	"mediamux/media"
)

// processSegment is the CAS/analysis decision for a single segment.
func (r *Runner) processSegment(ctx context.Context, in *Input, i int) (SegmentOutcome, []Event, error) {
	seg := in.Manifest.Segments[i]
	data := in.Segments[i]
	node := in.Nodes[i]
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
	var events []Event
	lookup := r.cache.Lookup(digest, expect)
	if lookup.Hit {
		var out SegmentOutcome
		if err := json.Unmarshal(lookup.Entry.Meta, &out); err == nil && out.Digest == digest {
			out.FromCache = true
			out.Node = node
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
		Quality: analyze.SegmentSample(segRes),
	}
	if i < len(in.RTP) && len(in.RTP[i]) > 0 {
		rtpRes := analyze.AnalyzeRTP(in.RTP[i], analyze.RTPOptions{ClockRate: 90000})
		out.RTP = rtpRes
		if rtpRes.Corrupt && !out.Corrupt {
			out.Corrupt = true
			out.Quality.CorruptMS = out.Quality.DurationMS
			out.Recovery = "transport gap located; container re-scanned clean"
		}
	}
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
