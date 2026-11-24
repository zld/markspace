package segment

import (
	"fmt"

	"mediapipe/internal/model"
)

// ClosedSegment pairs immutable segment metadata with the packets it contains.
type ClosedSegment struct {
	Segment model.Segment
	Packets []model.Packet
}

// Cutter selects segment boundaries at the nearest available packet start.
type Cutter struct {
	TargetMs int64

	originMs     int64
	nextTargetMs int64
	hasOrigin    bool

	seg     model.Segment
	packets []model.Packet
	active  bool

	closed            []model.Segment
	MaxAlignmentMs    int64
	AlignmentErrorsMs []int64
}

func NewCutter(targetMs int64) *Cutter {
	if targetMs <= 0 {
		targetMs = 2000
	}
	return &Cutter{TargetMs: targetMs}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func bitrate(bytes int64, durationMs int64) float64 {
	if durationMs <= 0 {
		return 0
	}
	return float64(bytes) * 8.0 * 1000.0 / float64(durationMs)
}

func (c *Cutter) start(p model.Packet) {
	c.active = true
	c.packets = []model.Packet{p}
	c.seg = model.Segment{
		Index:            len(c.closed),
		StartMs:          p.MediaMs,
		StartExtSequence: p.ExtSequence,
		EndExtSequence:   p.ExtSequence,
		Recoverable:      true,
	}
	if !c.hasOrigin {
		c.originMs = p.MediaMs
		c.nextTargetMs = p.MediaMs + c.TargetMs
		c.hasOrigin = true
	}
}

func (c *Cutter) recomputeStats(endMs int64) {
	var payloadBytes int64
	for _, p := range c.packets {
		payloadBytes += int64(len(p.Payload))
	}
	c.seg.PacketCount = len(c.packets)
	c.seg.PayloadBytes = payloadBytes
	c.seg.EndMs = endMs
	if len(c.packets) > 0 {
		last := c.packets[len(c.packets)-1]
		c.seg.EndExtSequence = last.ExtSequence
	}
	c.seg.BitrateBps = bitrate(payloadBytes, endMs-c.seg.StartMs)
}

func (c *Cutter) closeAt(boundaryMs, targetMs int64) ClosedSegment {
	c.recomputeStats(boundaryMs)
	alignment := abs64(boundaryMs - targetMs)
	c.seg.BoundaryAlignmentMs = alignment
	if alignment > c.MaxAlignmentMs {
		c.MaxAlignmentMs = alignment
	}
	c.AlignmentErrorsMs = append(c.AlignmentErrorsMs, alignment)
	c.seg.Complete = true
	c.seg.Recoverable = c.seg.PacketCount > 0

	out := ClosedSegment{Segment: c.seg, Packets: append([]model.Packet(nil), c.packets...)}
	c.closed = append(c.closed, out.Segment)
	c.active = false
	c.packets = nil
	c.seg = model.Segment{}
	c.nextTargetMs += c.TargetMs
	return out
}

// Push adds a packet and returns a segment if this packet causes a boundary.
// To choose the nearest packet boundary, it may move the previous packet into
// the new segment.
func (c *Cutter) Push(p model.Packet) *ClosedSegment {
	if !c.active {
		c.start(p)
		return nil
	}

	if p.MediaMs < c.nextTargetMs {
		c.packets = append(c.packets, p)
		return nil
	}

	prev := c.packets[len(c.packets)-1]
	prevDistance := abs64(prev.MediaMs - c.nextTargetMs)
	curDistance := abs64(p.MediaMs - c.nextTargetMs)
	target := c.nextTargetMs

	if prevDistance <= curDistance && len(c.packets) > 1 {
		// Boundary belongs at prev: remove it from the closing segment.
		c.packets = c.packets[:len(c.packets)-1]
		closed := c.closeAt(prev.MediaMs, target)
		c.start(prev)
		c.packets = append(c.packets, p)
		return &closed
	}

	closed := c.closeAt(p.MediaMs, target)
	c.start(p)
	return &closed
}

// Finish closes the tail. A truncated tail is retained but marked incomplete.
func (c *Cutter) Finish(truncated bool) *ClosedSegment {
	if !c.active || len(c.packets) == 0 {
		return nil
	}
	last := c.packets[len(c.packets)-1]
	frameMs := last.FrameMs
	if frameMs <= 0 {
		frameMs = 20
	}
	endMs := last.MediaMs + frameMs
	c.recomputeStats(endMs)
	c.seg.Complete = !truncated && endMs-c.seg.StartMs >= c.TargetMs
	c.seg.Recoverable = c.seg.PacketCount > 0
	if truncated {
		c.seg.Complete = false
	}
	out := ClosedSegment{Segment: c.seg, Packets: append([]model.Packet(nil), c.packets...)}
	c.closed = append(c.closed, out.Segment)
	c.active = false
	return &out
}

func (c *Cutter) Segments() []model.Segment {
	return append([]model.Segment(nil), c.closed...)
}

// ValidateAlignment is a small helper used by verify.
func ValidateAlignment(segments []model.Segment, expectedMs int64) (int64, error) {
	var max int64
	for i, s := range segments {
		if i > 0 {
			gap := s.StartMs - segments[i-1].EndMs
			if abs64(gap) > expectedMs {
				return max, fmt.Errorf("segment %d gap %d exceeds tolerance %d", i, gap, expectedMs)
			}
		}
		if s.BoundaryAlignmentMs > max {
			max = s.BoundaryAlignmentMs
		}
	}
	return max, nil
}
