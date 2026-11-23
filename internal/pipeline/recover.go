// Package pipeline wires the stages together. This file recovers each
// representation through its own RTP path and plans the switch.
package pipeline

import (
	"context"
	"sort"

	"mediad/internal/fanin"
	"mediad/internal/fanout"
	"mediad/internal/media"
	"mediad/internal/rtp"
)

// recoverStreams sends the repaired blocks through each layer's transport
// model and RTP reorder window.
//
// Recovery works from the repaired stream, so every representation starts
// from the same bytes and only its own transport behaviour differs. A layer
// that cancels is dropped from the merge; the others are unaffected.
func recoverStreams(ctx context.Context, blocks []media.Block, opts Options) (
	streams map[string][]media.Block, total rtp.Stats, absent map[string]bool) {

	streams = make(map[string][]media.Block, len(fanout.LadderNames))
	absent = make(map[string]bool)
	// A cancelled consumer keeps only the blocks it received before walking
	// away, so its representation contributes a confirmed prefix.
	keep := len(blocks)
	if opts.CancelLayer != "" && len(blocks) >= 3 {
		keep = len(blocks) / 3
	}
	bySeq := make(map[uint16]media.Block, len(blocks))
	for _, b := range blocks {
		bySeq[uint16(b.Seq)] = b
	}

	for _, name := range fanout.LadderNames {
		transp := fanout.Transport{}
		if tr, ok := opts.LayerTransport[name]; ok {
			transp = tr
		}
		src := blocks
		if name == opts.CancelLayer {
			src = blocks[:keep]
			absent[name] = true
		}
		wire := make([]rtp.Packet, 0, len(src))
		var delayed *rtp.Packet
		for _, b := range src {
			if err := ctx.Err(); err != nil {
				break
			}
			seq := uint16(b.Seq)
			if transp.DropEvery > 0 && int(b.Seq+1)%transp.DropEvery == 0 {
				continue
			}
			p := rtp.Packet{Sequence: seq, PayloadLen: b.Size()}
			if transp.SwapEvery > 0 && int(b.Seq+1)%transp.SwapEvery == 0 {
				held := p
				delayed = &held
				continue
			}
			if delayed != nil {
				wire = append(wire, p, *delayed)
				delayed = nil
				continue
			}
			wire = append(wire, p)
		}
		ordered, stats := rtp.RecoverPackets(wire, opts.ReorderWindow)
		out := make([]media.Block, 0, len(ordered))
		for _, p := range ordered {
			b, ok := bySeq[p.Sequence]
			if !ok {
				continue
			}
			cp := b
			cp.Rep = name
			out = append(out, cp)
		}
		streams[name] = fanin.SortStream(out)
		total.Delivered += stats.Delivered
		total.Lost += stats.Lost
		total.Recovered += stats.Recovered
		total.Duplicate += stats.Duplicate
		if stats.MaxReorder > total.MaxReorder {
			total.MaxReorder = stats.MaxReorder
		}
		if !total.HaveRange && stats.HaveRange {
			total.FirstSeq, total.LastSeq, total.HaveRange = stats.FirstSeq, stats.LastSeq, true
		}
	}
	return streams, total, absent
}

// buildPlan places one switch onto the middle representation at a fraction of
// the clip, snapped to a video block so both sides carry the instant.
func buildPlan(blocks []media.Block, order []string, fraction float64, absent map[string]bool) []fanin.Switch {
	if len(blocks) == 0 || len(order) < 2 || absent[order[1]] {
		return nil
	}
	var video []media.Block
	for _, b := range blocks {
		if b.TrackType == media.TrackVideo {
			video = append(video, b)
		}
	}
	if len(video) == 0 {
		return nil
	}
	sort.Slice(video, func(i, j int) bool { return video[i].TimeNs < video[j].TimeNs })
	idx := int(float64(len(video)-1) * fraction)
	return []fanin.Switch{{Track: video[idx].Track, AtNs: video[idx].TimeNs, ToRep: order[1]}}
}
