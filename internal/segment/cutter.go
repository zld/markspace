// Package segment turns a parsed transport stream into aligned,
// bounded-memory media segments plus a verifiable manifest. The cutter never
// holds more than one segment per representation at a time; alignment maps
// every representation onto one shared segment grid whose maximum drift is
// bounded by one input packet duration.
package segment

import (
	"crypto/sha256"
	"encoding/hex"

	"mediad/internal/model"
)

// Descriptor is the durable record of one produced segment. Bytes are
// counted as packets accumulate; the SHA-256 is finalized once the muxer
// finishes writing, and is what verify re-checks.
type Descriptor struct {
	Representation string `json:"representation"`
	Index          int    `json:"index"`
	StartTicks     int64  `json:"start_ticks"`
	EndTicks       int64  `json:"end_ticks"`
	StartOffset    int64  `json:"start_offset"`
	EndOffset      int64  `json:"end_offset"`
	PacketCount    int    `json:"packet_count"`
	ByteSize       int64  `json:"byte_size"`
	SHA256         string `json:"sha256"`
	// Corrupt reports PIDs known unreliable anywhere within this segment.
	Corrupt []uint16 `json:"corrupt_pids,omitempty"`
}

// TimelineView abstracts the clock so the segment package does not depend on
// the concrete clock implementation.
type TimelineView interface {
	MediaTicks() int64
	AffectedPIDs(mediaTicks int64) []uint16
}

// Cutter assigns incoming packets to segments on the shared 27 MHz grid.
// One Cutter serves one representation; Align coordinates several cutters.
// It is single-goroutine and retains only the running digest plus one open
// descriptor, never a full segment's packets.
type Cutter struct {
	repID       string
	base        model.TimeBase
	timeline    TimelineView
	current     *Descriptor
	segments    []*Descriptor
	hasher      sha256Hash
	// adoptedThrough is the highest index restored from a checkpoint.
	// Replayed segments at or below this index are not re-appended.
	adoptedThrough int
	// PacketTicks is the nominal duration represented by one input packet,
	// used as the alignment tolerance.
	PacketTicks int64
}

// sha256Hash is a small indirection over crypto/sha256 so the running state
// stays explicit: hash state is O(1) memory regardless of segment size.
type sha256Hash interface {
	Write(p []byte) (int, error)
	Sum(b []byte) []byte
	Reset()
}

// NewCutter creates a cutter for one representation.
func NewCutter(repID string, base model.TimeBase, tl TimelineView, packetTicks int64) *Cutter {
	return &Cutter{
		repID:       repID,
		base:        base,
		timeline:    tl,
		PacketTicks: packetTicks,
	}
}

// AddPacket folds one packet into the open segment, opening a new segment at
// every grid boundary. It returns the descriptor that was closed, or nil when
// the packet extended the current segment.
func (c *Cutter) AddPacket(p *model.Packet) (*Descriptor, error) {
	media := c.timeline.MediaTicks()
	segIdx := c.base.SegmentIndex(media)
	var closed *Descriptor
	if c.current == nil {
		c.open(segIdx, p.Offset)
	} else if segIdx != c.current.Index {
		closed = c.closeCurrent(media, p.Offset)
		c.open(segIdx, p.Offset)
	}
	c.current.PacketCount++
	c.current.EndOffset = p.Offset + model.PacketSize
	c.current.EndTicks = media
	if pids := c.timeline.AffectedPIDs(media); len(pids) > 0 {
		c.current.Corrupt = mergePIDs(c.current.Corrupt, pids)
	}
	_, _ = c.hasher.Write(p.Raw[:])
	return closed, nil
}

func (c *Cutter) open(idx int, offset int64) {
	h := sha256.New()
	c.hasher = h
	c.current = &Descriptor{
		Representation: c.repID,
		Index:          idx,
		StartTicks:     c.base.SegmentStart(idx),
		StartOffset:    offset,
	}
}

func (c *Cutter) closeCurrent(media, nextOffset int64) *Descriptor {
	d := c.current
	d.EndTicks = media
	if nextOffset > d.StartOffset {
		d.EndOffset = nextOffset
	}
	d.SHA256 = hex.EncodeToString(c.hasher.Sum(nil))
	// During resume replay every segment is recomputed exactly once, so the
	// descriptor is appended once regardless of whether it was adopted; the
	// engine's checkpoint ledger decides whether the file is re-muxed.
	c.segments = append(c.segments, d)
	c.current = nil
	return d
}

// Flush closes the segment still open at end of stream.
func (c *Cutter) Flush() *Descriptor {
	if c.current == nil {
		return nil
	}
	return c.closeCurrent(c.current.EndTicks, c.current.EndOffset)
}

// Segments returns all closed descriptors in order.
func (c *Cutter) Segments() []*Descriptor { return c.segments }

// NextIndex returns the segment index the next packet would open if it were
// added now. It lets the pipeline apply an interrupt gate before a new
// segment is created rather than after it has already been committed.
func (c *Cutter) NextIndex(mediaTicks int64) int {
	return c.base.SegmentIndex(mediaTicks)
}

// Adopt marks segment index d.Index as already produced and committed in an
// earlier run. It records only the replay watermark: no placeholder
// descriptor is inserted, because the resumed run recomputes every
// descriptor from the identical input, then reuses the ledger record for
// the on-disk file and its digest.
func (c *Cutter) Adopt(d *Descriptor) {
	if d.Index > c.adoptedThrough {
		c.adoptedThrough = d.Index
	}
}

// mergePIDs unions two PID lists without duplicates.
func mergePIDs(a, b []uint16) []uint16 {
	seen := make(map[uint16]struct{}, len(a)+len(b))
	out := append([]uint16(nil), a...)
	for _, pid := range out {
		seen[pid] = struct{}{}
	}
	for _, pid := range b {
		if _, ok := seen[pid]; !ok {
			seen[pid] = struct{}{}
			out = append(out, pid)
		}
	}
	return out
}
