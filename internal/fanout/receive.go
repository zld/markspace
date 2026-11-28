package fanout

import (
	"github.com/pion/rtp"

	"mediapack/internal/render"
)

// receiveResult is the raw reassembly output before segment bookkeeping.
type receiveResult struct {
	ordered    []*rtp.Packet // unique, sequence-ordered
	duplicates int
	reordered  bool
	gaps       []Gap
}

// receive deduplicates, reorders and detects missing sequence ranges.
// expectedCount is the number of packets the sender emitted; without it the
// tail-loss gap could not be distinguished from a normal stream end. seqToSeg
// attributes every lost sequence to its segment even when the whole segment
// was dropped.
func receive(stream []*rtp.Packet, expectedCount int, seqToSeg []int) receiveResult {
	seen := map[uint16]*rtp.Packet{}
	dups := 0
	reordered := false
	for pos, p := range stream {
		if _, ok := seen[p.SequenceNumber]; ok {
			dups++
			continue
		}
		if pos > 0 && p.SequenceNumber < stream[pos-1].SequenceNumber {
			reordered = true
		}
		seen[p.SequenceNumber] = p
	}
	ordered := make([]*rtp.Packet, 0, len(seen))
	for i := 0; i < expectedCount; i++ {
		if p, ok := seen[uint16(i)]; ok {
			ordered = append(ordered, p)
		}
	}
	var gaps []Gap
	for i := 0; i < expectedCount; {
		if _, ok := seen[uint16(i)]; ok {
			i++
			continue
		}
		start := i
		for i < expectedCount {
			if _, ok := seen[uint16(i)]; ok {
				break
			}
			i++
		}
		end := i - 1
		seg := 0
		if start < len(seqToSeg) {
			seg = seqToSeg[start]
		}
		gaps = append(gaps, Gap{
			SegmentIndex: seg,
			StartSeq:     uint16(start),
			EndSeq:       uint16(end),
			Count:        end - start + 1,
		})
	}
	return receiveResult{
		ordered: ordered, duplicates: dups, reordered: reordered, gaps: gaps,
	}
}

// buildSegments reconstructs per-segment metadata from ordered packets and
// attaches the detected gaps. Frames are emitted once per marker packet.
func buildSegments(layer LayerSpec, frames []render.Frame, rec receiveResult) (*Representation, error) {
	rep := &Representation{
		Layer:      layer,
		Received:   len(rec.ordered),
		Duplicates: rec.duplicates,
		Reordered:  rec.reordered,
		Gaps:       rec.gaps,
	}
	gapBySeg := map[int][]Gap{}
	for _, g := range rec.gaps {
		gapBySeg[g.SegmentIndex] = append(gapBySeg[g.SegmentIndex], g)
	}
	segCount := 0
	if len(frames) > 0 {
		segCount = (len(frames)-1)/layer.FramesPerSeg + 1
	}
	seqsBySeg := map[int][]uint16{}
	for _, p := range rec.ordered {
		h, err := decodeHeader(p.Payload)
		if err != nil {
			return nil, err
		}
		seqsBySeg[int(h.segment)] = append(seqsBySeg[int(h.segment)], p.SequenceNumber)
		if p.Marker {
			rep.Frames = append(rep.Frames, FrameRef{
				SegmentIndex: int(h.segment),
				FrameIndex:   int(h.frame),
				Tick:         int64(h.tick),
				Hash:         frameHashAt(frames, int(h.frame)),
			})
		}
	}
	for si := 0; si < segCount; si++ {
		fStart := si * layer.FramesPerSeg
		fEnd := fStart + layer.FramesPerSeg - 1
		if fEnd >= len(frames) {
			fEnd = len(frames) - 1
		}
		seg := Segment{
			Index:      si,
			Layer:      layer.Name,
			FrameStart: fStart,
			FrameEnd:   fEnd,
			Seqs:       seqsBySeg[si],
			Gaps:       gapBySeg[si],
		}
		seg.TickStart = frames[fStart].Tick
		seg.TickEnd = frames[fEnd].Tick
		seg.Complete = len(seg.Gaps) == 0
		rep.Segments = append(rep.Segments, seg)
	}
	if len(rec.ordered) > 0 {
		rep.FirstSeq = rec.ordered[0].SequenceNumber
		rep.LastSeq = rec.ordered[len(rec.ordered)-1].SequenceNumber
	}
	return rep, nil
}

func frameHashAt(frames []render.Frame, idx int) string {
	if idx >= 0 && idx < len(frames) {
		return frames[idx].Hash
	}
	return ""
}
