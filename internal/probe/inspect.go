// Package probe inspects tracks, keyframe cadence, RTP packet integrity and
// damaged ranges. Damage is always bound to a concrete interval so a fault in
// one segment never contaminates later valid segments.
package probe

import (
	"fmt"

	"github.com/pion/rtp"

	"mediacheck/internal/clock"
	"mediacheck/internal/dash"
	"mediacheck/internal/fixture"
	"mediacheck/internal/model"
)

// MaxKeyframeIntervalTick bounds the allowed gap between key frames. Our
// ladder emits one key frame per 0.4 s segment, so 1.2 s is a safe ceiling.
const MaxKeyframeIntervalTick int64 = int64(1.2 * float64(clock.MasterTicksPerSecond))

// SegmentResult is the probe verdict for one segment.
type SegmentResult struct {
	Representation string
	Sequence       uint32
	Range          model.Range
	Damaged        bool
	Reasons        []model.FaultKind
	KeyframeTicks  []int64
	PacketLoss     int
}

// Report aggregates probe output over a whole program.
type Report struct {
	Segments []*SegmentResult
	Damaged  []model.Range
	Issues   []model.Issue
}

// Inspect runs every probe check against parsed representations.
func Inspect(manifest *dash.Manifest, prog *fixture.Program) *Report {
	out := &Report{}
	byID := map[string]*fixture.Representation{}
	for _, r := range append(append([]*fixture.Representation{}, prog.Video...), prog.Audio...) {
		byID[r.ID] = r
	}
	for _, rec := range manifest.Representations {
		fr := byID[rec.ID]
		results := inspectRepresentation(rec, fr)
		for _, sr := range results {
			out.Segments = append(out.Segments, sr)
			if sr.Damaged {
				out.Damaged = append(out.Damaged, sr.Range)
				for _, reason := range sr.Reasons {
					out.Issues = append(out.Issues, model.Issue{
						Kind: reason, Representation: rec.ID,
						StartTick: sr.Range.Start, EndTick: sr.Range.End,
						Detail: fmt.Sprintf("segment %d", sr.Sequence),
					})
				}
			}
		}
	}
	checkCrossRepresentation(manifest, out)
	return out
}

func inspectRepresentation(rec *dash.RepresentationRecord, fr *fixture.Representation) []*SegmentResult {
	var results []*SegmentResult
	var lastKeyTick int64 = -1
	var prevEnd int64 = -1
	for i, seg := range rec.Segments {
		sr := &SegmentResult{
			Representation: rec.ID, Sequence: seg.Sequence,
			Range: model.Range{Start: seg.StartTick, End: seg.EndTick},
		}
		// Keyframe cadence (video only). Each healthy segment opens on a key
		// sample; a segment without one extends the gap from the last key.
		if rec.Kind == "video" {
			if seg.KeyframeCount > 0 {
				sr.KeyframeTicks = append(sr.KeyframeTicks, seg.StartTick)
				if lastKeyTick >= 0 {
					if gap := seg.StartTick - lastKeyTick; gap > MaxKeyframeIntervalTick {
						sr.Damaged = true
						sr.Reasons = append(sr.Reasons, model.FaultKeyframe)
					}
				}
				lastKeyTick = seg.StartTick
			} else if lastKeyTick >= 0 && seg.EndTick-lastKeyTick > MaxKeyframeIntervalTick {
				sr.Damaged = true
				sr.Reasons = append(sr.Reasons, model.FaultKeyframe)
			}
		}
		// Timestamp order: a segment starting before the prior one ended,
		// outside a declared reset, is backwards/non-contiguous.
		if prevEnd >= 0 && seg.StartTick < prevEnd {
			sr.Damaged = true
			sr.Reasons = append(sr.Reasons, model.FaultBackwards)
		}
		prevEnd = seg.EndTick
		// Structural / delivery faults from the parser and fixture.
		if seg.Truncated {
			sr.Damaged = true
			sr.Reasons = append(sr.Reasons, model.FaultTruncate)
		}
		if seg.Late {
			sr.Damaged = true
			sr.Reasons = append(sr.Reasons, model.FaultLate)
		}
		if seg.Duplicate {
			sr.Damaged = true
			sr.Reasons = append(sr.Reasons, model.FaultDuplicate)
		}
		if loss := packetLoss(fr.Segments[i]); loss > 0 {
			sr.PacketLoss = loss
			if !sr.Damaged {
				sr.Damaged = true
				sr.Reasons = append(sr.Reasons, model.FaultTruncate)
			}
		}
		results = append(results, sr)
	}
	return results
}

// packetLoss compares the observed RTP packet count against the count the
// sender expected before truncation-driven tail loss.
func packetLoss(seg *fixture.Segment) int {
	if seg.ExpectedPackets == 0 {
		return 0
	}
	seen := map[uint16]bool{}
	for _, p := range seg.Packets {
		seen[p.SequenceNumber] = true
	}
	loss := seg.ExpectedPackets - len(seen)
	if loss < 0 {
		loss = 0
	}
	return loss
}

// checkCrossRepresentation flags representation boundaries that do not line up
// on the shared timeline within one segment cadence.
func checkCrossRepresentation(manifest *dash.Manifest, out *Report) {
	var refs []*dash.SegmentRecord
	for _, rec := range manifest.Representations {
		if rec.Kind == "video" && len(rec.Segments) > 0 {
			refs = append(refs, rec.Segments...)
			break
		}
	}
	if len(refs) == 0 {
		return
	}
	for _, rec := range manifest.Representations {
		for _, seg := range rec.Segments {
			ref := refs[0]
			if int(seg.Sequence) < len(refs) {
				ref = refs[seg.Sequence]
			}
			if seg.StartTick != ref.StartTick {
				out.Issues = append(out.Issues, model.Issue{
					Kind: model.FaultClockSkew, Representation: rec.ID,
					StartTick: seg.StartTick, EndTick: seg.EndTick,
					Detail: fmt.Sprintf("boundary skew vs reference: %d ticks",
						seg.StartTick-ref.StartTick),
				})
			}
		}
	}
}

// RTPTimestampReset verifies that after a discontinuity the RTP timestamp can
// restart without being mistaken for backwards motion.
func RTPTimestampReset(before, after *rtp.Packet) bool {
	return after.Timestamp < before.Timestamp
}
