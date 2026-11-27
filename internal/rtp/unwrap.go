package rtp

import (
	"fmt"

	"rtx/internal/model"
)

// MaxForwardJump is the largest plausible forward RTP timestamp step
// between consecutive packets: one hour of a 90 kHz clock. Larger steps
// are treated as jumps, not wraps.
const MaxForwardJump int64 = 90_000 * 3600

// Point is one packet with its continuous unwrapped timestamp. Raw keeps
// the original 32-bit value; unwrapping never rewrites inputs.
type Point struct {
	Packet    Packet
	Raw       uint32
	Unwrapped uint64
}

// UnwrapResult holds the continuous timeline plus isolated ranges.
type UnwrapResult struct {
	Points    []Point
	Anomalies []model.Anomaly
	Isolated  []model.Range // half-open packet-index ranges excluded from timing
}

// Unwrap extends 32-bit RTP timestamps onto a continuous 64-bit line.
// Unwrapping only applies when sequence numbers are continuous and the
// wrap-aware timestamp difference is a plausible forward step. Backwards
// timestamps, sudden jumps and illegal wraps isolate the affected packet
// range: the packets keep their raw values and offsets but contribute no
// timing points. After an anomaly the next continuous packet re-anchors
// the timeline without emitting a point.
func Unwrap(packets []Packet) *UnwrapResult {
	res := &UnwrapResult{}
	if len(packets) == 0 {
		return res
	}
	base := uint64(0)
	prev := packets[0]
	prevUnwrapped := uint64(prev.Timestamp)
	res.Points = append(res.Points, Point{Packet: prev, Raw: prev.Timestamp, Unwrapped: prevUnwrapped})
	isolatedStart := -1
	reanchor := false
	isolate := func(i int, a model.Anomaly) {
		res.Anomalies = append(res.Anomalies, a)
		if isolatedStart < 0 {
			isolatedStart = i
		}
		reanchor = true
	}
	for i := 1; i < len(packets); i++ {
		cur := packets[i]
		gap := seqDiff(prev.Seq, cur.Seq)
		if gap != 1 {
			isolate(i, model.Anomaly{
				Stage: model.StageUnwrap, Reason: model.ReasonSeqGap,
				Offset: cur.Offset, RawValue: int64(cur.Seq),
				Message:  fmt.Sprintf("sequence jump %d -> %d", prev.Seq, cur.Seq),
				Recovery: "isolate range; resume unwrapping at next continuous pair",
			})
			prev = cur
			continue
		}
		if reanchor {
			// Best-effort re-anchor on the previous base; this packet
			// closes the isolated range but emits no timing point.
			prevUnwrapped = base + uint64(cur.Timestamp)
			prev = cur
			reanchor = false
			continue
		}
		d := model.WrapDiff(prev.Timestamp, cur.Timestamp)
		switch {
		case d >= 0 && d <= MaxForwardJump:
			if isolatedStart >= 0 {
				res.Isolated = append(res.Isolated, model.Range{Start: int64(isolatedStart), End: int64(i)})
				isolatedStart = -1
			}
			unwrapped := prevUnwrapped + uint64(d)
			res.Points = append(res.Points, Point{Packet: cur, Raw: cur.Timestamp, Unwrapped: unwrapped})
			prevUnwrapped = unwrapped
			base = unwrapped - uint64(cur.Timestamp)
		case d < 0:
			reason := model.ReasonBackwards
			if d < -MaxForwardJump {
				reason = model.ReasonIllegalWrap
			}
			isolate(i, model.Anomaly{
				Stage: model.StageUnwrap, Reason: reason,
				Offset: cur.Offset, RawValue: int64(cur.Timestamp),
				Message:  fmt.Sprintf("timestamp moved backwards by %d ticks", -d),
				Recovery: "isolate packet; keep raw timestamp",
			})
		default:
			isolate(i, model.Anomaly{
				Stage: model.StageUnwrap, Reason: model.ReasonJump,
				Offset: cur.Offset, RawValue: int64(cur.Timestamp),
				Message:  fmt.Sprintf("timestamp jumped forward by %d ticks", d),
				Recovery: "isolate packet; keep raw timestamp",
			})
		}
		prev = cur
	}
	if isolatedStart >= 0 {
		res.Isolated = append(res.Isolated, model.Range{Start: int64(isolatedStart), End: int64(len(packets))})
	}
	return res
}
