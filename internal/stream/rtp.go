package stream

import (
	"fmt"

	"github.com/pion/rtp"

	"mediascan/internal/clock"
	"mediascan/internal/fault"
)

// RTPPacketInfo is the normalized view of one parsed RTP packet.
type RTPPacketInfo struct {
	Index         int       `json:"index"`
	Sequence      uint16    `json:"sequence"`
	WrappedTS     uint32    `json:"wrapped_ts"`
	ExpandedTS    int64     `json:"expanded_ts"`
	Time          clock.Rat `json:"time_seconds"`
	Marker        bool      `json:"marker"`
	PayloadLength int       `json:"payload_length"`
}

// RTPResult captures a parsed packet stream on the unified time base.
type RTPResult struct {
	ClockRate     uint32            `json:"clock_rate"`
	Packets       []RTPPacketInfo   `json:"packets"`
	Wraps         []clock.WrapEvent `json:"wrap_events"`
	NegativeJumps []fault.Record    `json:"negative_jumps"`
	// FrameIntervals are actual inter-frame gaps in seconds (VFR-aware:
	// measured from packet timestamps, never a fixed-rate assumption).
	FrameIntervals []float64 `json:"frame_intervals_seconds"`
	// MeanFrameRate is total frames divided by measured span.
	MeanFrameRate float64 `json:"mean_frame_rate"`
}

// ParseRTP decodes a sequence of RTP packets, expands 32-bit timestamp
// rollovers through the clock unwrapper and maps every packet onto the
// unified rational time base. Negative jumps are recorded as faults and
// never treated as new segments.
func ParseRTP(packets [][]byte, clockRate uint32) (*RTPResult, error) {
	if clockRate == 0 {
		return nil, fmt.Errorf("stream: RTP clock rate must be non-zero")
	}
	res := &RTPResult{ClockRate: clockRate}
	unwrapper := clock.NewUnwrapper()
	timeline := clock.NewTimeline(clockRate)
	var originSet bool
	var prevExpanded int64

	for i, raw := range packets {
		var pkt rtp.Packet
		if err := pkt.Unmarshal(raw); err != nil {
			return res, fmt.Errorf("stream: RTP packet %d unmarshal failed: %w", i, err)
		}
		expanded, err := unwrapper.Expand(pkt.Timestamp)
		if err != nil {
			res.NegativeJumps = append(res.NegativeJumps, fault.Record{
				Stage: fault.StageTransform, Severity: fault.SeverityError,
				Kind: fault.KindTimestampJump, MediaSeq: uint64(pkt.SequenceNumber),
				ByteOffset: int64(i),
				Message:    err.Error(),
			})
			// Keep the previously established monotonic line instead of
			// resynchronizing on the bad sample.
			expanded = prevExpanded
		}
		if !originSet {
			timeline.SetOrigin(expanded)
			originSet = true
		}
		info := RTPPacketInfo{
			Index: i, Sequence: pkt.SequenceNumber,
			WrappedTS: pkt.Timestamp, ExpandedTS: expanded,
			Time: timeline.At(expanded), Marker: pkt.Marker,
			PayloadLength: len(pkt.Payload),
		}
		if i > 0 {
			deltaTicks := expanded - prevExpanded
			if deltaTicks >= 0 {
				res.FrameIntervals = append(res.FrameIntervals,
					clock.NewRat(deltaTicks, int64(clockRate)).Seconds())
			}
		}
		res.Packets = append(res.Packets, info)
		prevExpanded = expanded
	}
	res.Wraps = unwrapper.Wraps()
	res.MeanFrameRate = computeMeanFrameRate(res.FrameIntervals)
	return res, nil
}

// computeMeanFrameRate derives rate from the measured span so VFR input
// is summarized without assuming a constant cadence.
func computeMeanFrameRate(intervals []float64) float64 {
	if len(intervals) == 0 {
		return 0
	}
	var span float64
	for _, d := range intervals {
		span += d
	}
	if span <= 0 {
		return 0
	}
	return float64(len(intervals)+1) / span
}

// WrapFaults converts observed rollover events into informational records.
func (r *RTPResult) WrapFaults() []fault.Record {
	out := make([]fault.Record, 0, len(r.Wraps))
	for _, w := range r.Wraps {
		out = append(out, fault.Record{
			Stage: fault.StageTransform, Severity: fault.SeverityWarning,
			Kind: fault.KindWrapExpanded, ByteOffset: int64(w.Offset),
			Message: fmt.Sprintf("timestamp wrap expanded: %#x -> %#x = %d",
				w.FromWrapped, w.ToWrapped, w.Expanded),
		})
	}
	return out
}
