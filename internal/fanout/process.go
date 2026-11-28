package fanout

import (
	"github.com/pion/rtp"

	"mediapack/internal/render"
	"mediapack/internal/timeline"
)

// LayerResult is one representation together with its current resume anchor.
// On a first run open gaps remain open (no fabrication); on resume the exact
// missing sequences are retransmitted from the original packet set and marked
// recovered, which closes the corresponding gaps.
type LayerResult struct {
	Rep           *Representation
	Frames        []render.Frame
	LastSegment   int
	LastFrame     int
	LastSeq       uint16
	RecoveredGaps int
}

// ProcessLayer renders, packetizes and reassembles one layer. When
// retransmit is false the initial impairment simulation stands and lost
// packets stay recorded as gaps; when true the original packets behind every
// gap are replayed reliably (no new media is invented) and the gaps close.
// maxSegments bounds how many segments may anchor the checkpoint (0 = all).
func ProcessLayer(layer LayerSpec, renderer *render.Renderer, tl *timeline.Timeline,
	cfg SimConfig, maxSegments int, retransmit bool) (*LayerResult, error) {
	frames, err := renderer.RenderLayer(layer.LayerConfig(), tl)
	if err != nil {
		return nil, err
	}
	packets, seqToSeg, err := packetize(layer, frames)
	if err != nil {
		return nil, err
	}
	stream := simulate(packets, cfg)
	rec := receive(stream, len(packets), seqToSeg)

	gaps := append([]Gap(nil), rec.gaps...)
	ordered := rec.ordered
	recoveredCount := 0
	if retransmit && len(gaps) > 0 {
		recovered := map[uint16]bool{}
		for _, g := range gaps {
			for seq := int(g.StartSeq); seq <= int(g.EndSeq); seq++ {
				if seq < len(packets) {
					recovered[uint16(seq)] = true
				}
			}
		}
		ordered = mergeRetransmissions(rec.ordered, packets, recovered)
		gaps = markRecovered(gaps, recovered)
		recoveredCount = len(recovered)
	}

	rep, err := buildSegments(layer, frames, receiveResult{
		ordered:    ordered,
		duplicates: rec.duplicates,
		reordered:  rec.reordered,
		gaps:       gaps,
	})
	if err != nil {
		return nil, err
	}
	rep.Sent = len(packets)
	rep.RecoveredGaps = recoveredCount

	result := &LayerResult{Rep: rep, Frames: frames, RecoveredGaps: recoveredCount}
	limit := len(rep.Segments)
	if maxSegments > 0 && maxSegments < limit {
		limit = maxSegments
	}
	lastSeg, lastFrame, lastSeq := -1, -1, uint16(0)
	for si := 0; si < limit; si++ {
		seg := rep.Segments[si]
		if !segmentFullyRecovered(seg.Gaps) {
			break
		}
		lastSeg = si
		lastFrame = seg.FrameEnd
		if len(seg.Seqs) > 0 {
			lastSeq = seg.Seqs[len(seg.Seqs)-1]
		}
	}
	result.LastSegment, result.LastFrame, result.LastSeq = lastSeg, lastFrame, lastSeq
	return result, nil
}

// mergeRetransmissions rebuilds the ordered packet set by adding the
// retransmitted originals to the packets that survived the first pass.
func mergeRetransmissions(survivors []*rtp.Packet, all []*rtp.Packet,
	recovered map[uint16]bool) []*rtp.Packet {
	have := map[uint16]bool{}
	for _, p := range survivors {
		have[p.SequenceNumber] = true
	}
	out := make([]*rtp.Packet, 0, len(all))
	for i, p := range all {
		if have[uint16(i)] || recovered[uint16(i)] {
			out = append(out, p)
		}
	}
	return out
}

// markRecovered annotates gaps whose sequences were retransmitted and drops
// the now-closed ones from the open-gap list.
func markRecovered(gaps []Gap, recovered map[uint16]bool) []Gap {
	var open []Gap
	for _, g := range gaps {
		closed := true
		for seq := int(g.StartSeq); seq <= int(g.EndSeq); seq++ {
			if !recovered[uint16(seq)] {
				closed = false
				break
			}
		}
		if !closed {
			open = append(open, g)
		}
	}
	return open
}

func segmentFullyRecovered(gaps []Gap) bool {
	for _, g := range gaps {
		if !g.Recovered {
			return false
		}
	}
	return true
}
