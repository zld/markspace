// Package mkv turns a parsed EBML document into Matroska/WebM tracks and
// interleaved blocks. Block payloads are never rewritten: every block keeps
// its absolute input offset, declared size and exact byte slice so later
// stages can hash and slice the original media bytes.
package mkv

import (
	"sort"

	"tracecheck/internal/ebml"
	"tracecheck/internal/serr"
)

// ClusterRef describes where a block's enclosing cluster sits in the file.
type ClusterRef struct {
	Index         int   // zero based cluster order within the segment
	Offset        int64 // absolute offset of the Cluster element ID
	PayloadOffset int64 // absolute start of the Cluster payload
	End           int64 // exclusive absolute end of the whole Cluster element
	Timestamp     int64 // cluster Timestamp in TimestampScale ticks
	UnknownSize   bool  // cluster used an unknown-length size VINT
}

// Block is one SimpleBlock or (Simple)Block inside a BlockGroup.
type Block struct {
	Cluster      ClusterRef
	TrackNumber  uint64
	Timecode     int16 // block-relative timecode; cluster timestamp + this
	Flags        byte
	Keyframe     bool
	Payload      []byte // frame bytes only, never copied from the input
	Offset       int64  // absolute offset of the block element ID
	PayloadStart int64  // absolute offset of the frame payload
	PayloadEnd   int64  // exclusive absolute end of the block element
	Size         int64  // declared block element size (header included)
	Index        int    // zero based order among all blocks read so far
	Grouped      bool   // came from a BlockGroup rather than SimpleBlock
}

// TimecodeTicks returns the block's absolute timestamp in TimestampScale ticks.
func (b Block) TimecodeTicks() int64 { return b.Cluster.Timestamp + int64(b.Timecode) }

// ParseBlocks walks every Cluster in doc and returns all complete blocks in
// stable reading order. The same-instant ordering key is (track, index); see
// SortStable. Truncated blocks are reported with serr.Error but the returned
// slice still holds every block decoded before the failure so callers can
// choose to keep partial results (for example to report a damaged fixture).
func ParseBlocks(doc *ebml.Document) ([]Block, error) {
	var blocks []Block
	clusters := doc.All(ebml.IDCluster)
	ordinal := 0
	for ci, cluster := range clusters {
		ref := ClusterRef{
			Index:         ci,
			Offset:        cluster.Offset,
			PayloadOffset: cluster.PayloadOffset,
			End:           cluster.PayloadEnd,
			UnknownSize:   cluster.UnknownSize,
		}
		if ts := directChild(doc, cluster, ebml.IDTimestamp); ts != nil {
			v, ok := ebml.ReadUint(doc.Payload(ts))
			if !ok {
				return blocks, serr.New(serr.CodeIllegalVINT, "cluster %d has a malformed Timestamp", ci).
					At(ts.Offset).WithPath(pathNames(doc, ts))
			}
			ref.Timestamp = int64(v)
		}
		for _, child := range doc.Children[cluster] {
			switch child.ID {
			case ebml.IDSimpleBlock:
				b, err := decodeBlockElement(doc, ref, child, false, ordinal)
				if err != nil {
					return blocks, err
				}
				blocks = append(blocks, b)
				ordinal++
			case ebml.IDBlockGroup:
				for _, gc := range doc.Children[child] {
					if gc.ID != ebml.IDBlock {
						continue
					}
					b, err := decodeBlockElement(doc, ref, gc, true, ordinal)
					if err != nil {
						return blocks, err
					}
					blocks = append(blocks, b)
					ordinal++
				}
			}
		}
	}
	return blocks, nil
}

// directChild returns the first direct child of parent with the given ID.
func directChild(doc *ebml.Document, parent *ebml.Element, id ebml.ElementID) *ebml.Element {
	for _, c := range doc.Children[parent] {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func pathNames(doc *ebml.Document, el *ebml.Element) []string {
	ids := doc.PathOf(el)
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = ebml.NameOf(id)
	}
	return names
}

// decodeBlockElement validates one block element's frame header and splits the
// payload. A block whose frame header or declared frame length is short of the
// element is reported as CodeTruncatedBlock and never returned, so truncated
// blocks do not enter the sample set.
func decodeBlockElement(doc *ebml.Document, ref ClusterRef, el *ebml.Element,
	grouped bool, index int) (Block, error) {
	raw := doc.Payload(el)
	if len(raw) == 0 {
		return Block{}, serr.New(serr.CodeTruncatedBlock, "block has an empty payload").
			At(el.Offset).WithPath(pathNames(doc, el))
	}
	track, width, reserved, perr := ebml.DecodeVINT(raw)
	if perr != nil || reserved {
		return Block{}, serr.New(serr.CodeTruncatedBlock, "block track number VINT is illegal").
			At(el.PayloadOffset).WithPath(pathNames(doc, el))
	}
	// track VINT + 2-byte timecode + flags byte.
	if len(raw) < width+3 {
		return Block{}, serr.New(serr.CodeTruncatedBlock,
			"block header needs %d bytes, have %d", width+3, len(raw)).
			At(el.PayloadOffset).WithPath(pathNames(doc, el))
	}
	timecode := int16(uint16(raw[width])<<8 | uint16(raw[width+1]))
	flags := raw[width+2]
	frameStart := width + 3
	frame := raw[frameStart:]
	b := Block{
		Cluster:      ref,
		TrackNumber:  track,
		Timecode:     timecode,
		Flags:        flags,
		Keyframe:     grouped || flags&0x80 != 0,
		Payload:      frame,
		Offset:       el.Offset,
		PayloadStart: el.PayloadOffset + int64(frameStart),
		PayloadEnd:   el.PayloadEnd,
		Size:         el.Size,
		Index:        index,
		Grouped:      grouped,
	}
	// A block element must contain at least a frame header; zero-length frame
	// data is allowed (it carries no samples but is still well formed).
	return b, nil
}

// SortStable orders blocks by time, then track number, then block index. It is
// the canonical ordering used for the index so that re-running a parse on the
// same bytes yields byte-identical ordering.
func SortStable(blocks []Block) {
	sort.SliceStable(blocks, func(i, j int) bool {
		a, b := blocks[i], blocks[j]
		if at, bt := a.TimecodeTicks(), b.TimecodeTicks(); at != bt {
			return at < bt
		}
		if a.TrackNumber != b.TrackNumber {
			return a.TrackNumber < b.TrackNumber
		}
		return a.Index < b.Index
	})
}

// BlocksByTrack groups blocks per track number, preserving input order.
func BlocksByTrack(blocks []Block) map[uint64][]Block {
	out := make(map[uint64][]Block)
	for _, b := range blocks {
		out[b.TrackNumber] = append(out[b.TrackNumber], b)
	}
	return out
}
