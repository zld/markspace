// Package segment validates RTP fragment boundaries against declared MP4
// segment boundaries. Timestamps are checked for continuity and wraparound on
// the shared rational clock; drift is reported with the raw byte offset of the
// offending packet, never silently corrected.
package segment

import (
	"fmt"

	"github.com/pion/rtp"

	"packcheck/internal/clock"
	"packcheck/internal/container"
	"packcheck/internal/model"
)

// ClockRate is the RTP clock rate used by the synthetic media streams.
const ClockRate = 90000

// Packet is one observed RTP fragment plus the byte offset it was read at.
type Packet struct {
	Raw        []byte
	ByteOffset int64
}

// Frame is a reconstructed frame boundary on the shared clock.
type Frame struct {
	Seq        uint16
	TS         uint32
	Start      clock.Rat
	ByteOffset int64
}

// BoundaryResult holds the reconstructed frames and any detected defects.
type BoundaryResult struct {
	Frames    []Frame
	PeakQueue int
	Issues    model.IssueLog
}

// ValidateRTP parses a packet stream and verifies sequence continuity,
// timestamp monotonicity and fragment (marker) structure.
func ValidateRTP(pkts []Packet, rate clock.Rate) *BoundaryResult {
	res := &BoundaryResult{}
	var prevSeq uint16
	var prevTS uint32
	havePrev := false
	for _, p := range pkts {
		var pkt rtp.Packet
		if err := pkt.Unmarshal(p.Raw); err != nil {
			res.Issues.Add(model.Issue{
				Code: model.CodeUnsupportedInput, Severity: model.SeverityError,
				Layer: model.LayerSegment, ByteOffset: p.ByteOffset,
				Message: fmt.Sprintf("rtp unmarshal failed: %v", err),
			})
			continue
		}
		if havePrev {
			if pkt.SequenceNumber != prevSeq+1 {
				res.Issues.Add(model.Issue{
					Code: model.CodeBoundaryDrift, Severity: model.SeverityError,
					Layer: model.LayerSegment, ByteOffset: p.ByteOffset,
					Message: fmt.Sprintf("sequence gap: got %d want %d",
						pkt.SequenceNumber, prevSeq+1),
				})
			}
			// RTP timestamps may legitimately repeat within a fragmented
			// frame; they must never move backwards within one rollover.
			if pkt.Timestamp < prevTS {
				res.Issues.Add(model.Issue{
					Code: model.CodeWraparound, Severity: model.SeverityError,
					Layer: model.LayerSegment, ByteOffset: p.ByteOffset,
					Message: fmt.Sprintf("timestamp rollback: %d < %d", pkt.Timestamp, prevTS),
				})
			}
		}
		res.Frames = append(res.Frames, Frame{
			Seq: pkt.SequenceNumber, TS: pkt.Timestamp,
			Start:      RTPToInstant(pkt.Timestamp, rate),
			ByteOffset: p.ByteOffset,
		})
		prevSeq, prevTS, havePrev = pkt.SequenceNumber, pkt.Timestamp, true
	}
	return res
}

// RTPToInstant maps an RTP timestamp onto the unified rational clock.
func RTPToInstant(ts uint32, _ clock.Rate) clock.Rat {
	return clock.MustRat(int64(ts), ClockRate)
}

// ContainerTickToRTP maps a container timescale tick onto RTP timestamp space.
func ContainerTickToRTP(tick uint64, timescale uint32) uint32 {
	return uint32(tick * ClockRate / uint64(timescale))
}

// CompareToDeclared reconciles reconstructed frames with the container's
// declared sidx boundaries. Each mismatch is one separately counted drift.
func CompareToDeclared(res *BoundaryResult, c *container.Container) {
	if len(c.Boundaries) == 0 {
		return
	}
	tickToFrame := make(map[uint32]bool, len(res.Frames))
	for _, f := range res.Frames {
		tickToFrame[f.TS] = true
	}
	for _, b := range c.Boundaries {
		// Map container ticks (timescale units) onto RTP timestamp space.
		rtpTick := ContainerTickToRTP(b.StartTick, c.MovieTimescale)
		if !tickToFrame[rtpTick] {
			res.Issues.Add(model.Issue{
				Code: model.CodeBoundaryDrift, Severity: model.SeverityError,
				Layer: model.LayerSegment,
				Message: fmt.Sprintf("declared boundary tick=%d (rtp=%d) has no matching fragment",
					b.StartTick, rtpTick),
			})
		}
	}
}

// CrossCheck compares boundary signatures across still-available
// representations. Aligned means every layer shares the identical RTP
// timestamp sequence; mismatches retain the layer names and offsets.
type LayerSignature struct {
	Representation string
	Timestamps     []uint32
	Healthy        bool
}

// CrossCheck returns aligned=true when all healthy layers agree.
func CrossCheck(layers []LayerSignature) (aligned bool, mismatches []model.Issue) {
	var reference []uint32
	for _, l := range layers {
		if !l.Healthy {
			continue
		}
		if reference == nil {
			reference = l.Timestamps
			continue
		}
		if !equalTS(reference, l.Timestamps) {
			aligned = false
			mismatches = append(mismatches, model.Issue{
				Code: model.CodeBoundaryDrift, Severity: model.SeverityError,
				Layer: model.LayerSegment, Representation: l.Representation,
				Message: "boundary signature diverges from reference layer",
			})
		}
	}
	return len(mismatches) == 0, mismatches
}

func equalTS(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
