package verifier

import (
	"math/big"

	"mediasync/internal/clock"
	"mediasync/internal/container"
	"mediasync/internal/stream"
)

// rtpClockRate is the fixed ticks-per-second of generated packet streams,
// matching the dialogue track's 1kHz media clock so both share one axis.
const rtpClockRate uint32 = 1000

// mediaGridSeconds maps a zero-based ordinal onto the dialogue media grid
// using each sample's real (VFR) duration. It is the reference position the
// packet at that ordinal should align to.
func mediaGridSeconds(dialogue *container.Track, ordinal int) *big.Rat {
	if ordinal >= len(dialogue.Marks) {
		ordinal = len(dialogue.Marks) - 1
	}
	return dialogue.Marks[ordinal].CalibSeconds
}

// receivedOrdered returns the received (non-hole) packets in sequence order,
// paired with their monotonic extended RTP timestamps.
func receivedOrdered(seq *stream.SequenceReport) []struct {
	Ordinal int
	ExtTS   int64
} {
	out := make([]struct {
		Ordinal int
		ExtTS   int64
	}, 0, len(seq.Ordered))
	ordinal := 0
	for _, p := range seq.Ordered {
		ext, ok := seq.ExtendedTS[p.Sequence]
		if !ok {
			continue
		}
		out = append(out, struct {
			Ordinal int
			ExtTS   int64
		}{ordinal, ext})
		ordinal++
	}
	return out
}

// measureDrift fits the linear mapping from the observed RTP clock onto the
// media grid. The first and last received packets define the anchors; both
// their observed tick and expected reference-second positions are exact
// rationals. A nil report means the window is too small to measure drift.
func measureDrift(dialogue *container.Track, tb *clock.Timebase, seq *stream.SequenceReport, driftPPM int64) (*clock.DriftReport, error) {
	received := receivedOrdered(seq)
	if len(received) < 2 {
		return nil, nil
	}
	first, last := received[0], received[len(received)-1]
	anchors := []clock.Anchor{
		{ObservedTicks: first.ExtTS, RefSeconds: mediaGridSeconds(dialogue, first.Ordinal)},
		{ObservedTicks: last.ExtTS, RefSeconds: mediaGridSeconds(dialogue, last.Ordinal)},
	}
	return clock.DetectDrift(tb, anchors, driftPPM)
}
