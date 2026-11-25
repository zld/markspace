package ebml

import (
	"encoding/binary"

	"mediacheck/internal/report"
)

// Block is one decoded Matroska/SimpleBlock or Block element. Order is the
// original byte order, including interleaved tracks.
type Block struct {
	Index              int
	ClusterIndex       int
	Element            *Element
	Track              uint64
	TrackVINTOffset    int
	RelativeTimecodeNS int64
	RelativeTicks      int16
	Flags              byte
	Keyframe           bool
	FrameLength        int
}

// Cluster is a located Cluster and the blocks directly inside it.
type Cluster struct {
	Index          int
	Element        *Element
	Timecode       uint64
	TimecodeOffset int
	Blocks         []*Block
}

// Document is the small EBML/Matroska projection used by the validators.
type Document struct {
	Limits           Limits
	EBMLHeader       *Element
	Segment          *Element
	TimecodeScale    uint64
	TimecodeScaleOff int
	SegmentChildren  []*Element
	TrackEntries     []*Element
	Clusters         []*Cluster
	Blocks           []*Block
}

// ParseDocument parses with DefaultLimits.
func ParseDocument(data []byte) (*Document, *report.Error) {
	return ParseDocumentWithLimits(data, DefaultLimits())
}

// ParseDocumentWithLimits locates Segment/Tracks/Cluster/Block structures and
// preserves raw interleaved block order.
func ParseDocumentWithLimits(data []byte, limits Limits) (*Document, *report.Error) {
	r := NewReader(data, limits)
	doc := &Document{Limits: limits, TimecodeScale: 1_000_000}
	if limits.MaxBlocks <= 0 {
		limits = DefaultLimits()
		r.limits = limits
		doc.Limits = limits
	}

	off := 0
	for off < len(data) {
		e, err := r.ReadElementAt(off, len(data))
		if err != nil {
			return nil, err
		}
		switch e.ID {
		case IDEBMLHeader:
			doc.EBMLHeader = e
			if err := r.ParseTree(e); err != nil {
				return nil, err
			}
		case IDSegment:
			doc.Segment = e
		default:
			// Samples begin with EBML header then Segment. Preserve other
			// top-level elements by bounds but do not let them hide Segment.
			if e.Known {
				if err := r.ParseTree(e); err != nil {
					return nil, err
				}
			}
		}
		off = nextRootOffset(e, len(data))
		if doc.Segment != nil {
			break
		}
	}
	if doc.Segment == nil {
		return nil, report.NewError(report.EBMLIllegalLength, "missing_segment", "", 0, 0,
			"input does not contain a Segment element")
	}

	segEnd := len(data)
	if doc.Segment.Known {
		segEnd = doc.Segment.End
	} else {
		doc.Segment.End = len(data)
	}
	if err := r.parseSegment(doc, doc.Segment.PayloadOffset, segEnd); err != nil {
		return nil, err
	}
	return doc, nil
}

func nextRootOffset(e *Element, dataLen int) int {
	if e.Known {
		return e.End
	}
	return dataLen
}

func (r *Reader) parseSegment(doc *Document, off, segEnd int) *report.Error {
	for off < segEnd {
		e, err := r.ReadElementAt(off, segEnd)
		if err != nil {
			return err
		}
		if !e.Known && e.ID != IDCluster {
			return report.NewError(report.EBMLIllegalLength, "unknown_length_not_allowed", HexID(e.ID), e.Offset, e.SizeOffset,
				"unknown-length element is only allowed for Segment and Cluster")
		}

		switch e.ID {
		case IDInfo:
			if err := r.ParseTree(e); err != nil {
				return err
			}
			doc.SegmentChildren = append(doc.SegmentChildren, e)
			if tc := FindFirst(e, IDTimecodeScale); tc != nil {
				v, derr := r.ReadUint(tc)
				if derr != nil {
					return derr
				}
				doc.TimecodeScale = v
				doc.TimecodeScaleOff = tc.PayloadOffset
			}
		case IDTracks:
			if err := r.ParseTree(e); err != nil {
				return err
			}
			doc.SegmentChildren = append(doc.SegmentChildren, e)
			doc.TrackEntries = append(doc.TrackEntries, FindChildren(e, IDTrackEntry)...)
		case IDCluster:
			next, err := r.parseCluster(doc, e, segEnd)
			if err != nil {
				return err
			}
			off = next
			continue
		default:
			if e.Known && isContainer(e.ID) {
				if err := r.ParseTree(e); err != nil {
					return err
				}
			}
			doc.SegmentChildren = append(doc.SegmentChildren, e)
		}
		if !e.Known {
			return report.NewError(report.EBMLUnknownBoundary, "unknown_boundary", HexID(e.ID), e.Offset, e.SizeOffset,
				"cannot determine end of unknown-length non-Cluster element")
		}
		off = e.End
	}
	return nil
}

func (r *Reader) parseCluster(doc *Document, e *Element, segEnd int) (int, *report.Error) {
	cluster := &Cluster{Index: len(doc.Clusters), Element: e}
	doc.Clusters = append(doc.Clusters, cluster)

	start := e.PayloadOffset
	end := e.End
	if !e.Known {
		end = segEnd
		e.End = segEnd
	}

	off := start
	for off < end {
		if !e.Known && atSegmentBoundary(r.data, off, segEnd, r.limits) {
			e.End = off
			return off, nil
		}
		child, err := r.ReadElementAt(off, end)
		if err != nil {
			return 0, err
		}
		if !child.Known {
			return 0, report.NewError(report.EBMLIllegalLength, "unknown_length_not_allowed", HexID(child.ID), child.Offset, child.SizeOffset,
				"unknown length is not legal inside a Cluster")
		}

		switch child.ID {
		case IDTimecode:
			v, derr := r.ReadUint(child)
			if derr != nil {
				return 0, derr
			}
			cluster.Timecode = v
			cluster.TimecodeOffset = child.PayloadOffset
		case IDSimpleBlock:
			b, derr := r.parseBlock(child, cluster.Index, len(doc.Blocks))
			if derr != nil {
				return 0, derr
			}
			b.ClusterIndex = cluster.Index
			cluster.Blocks = append(cluster.Blocks, b)
			doc.Blocks = append(doc.Blocks, b)
			if len(doc.Blocks) > r.limits.MaxBlocks {
				return 0, report.NewError(report.EBMLLimit, "too_many_blocks", HexID(child.ID), child.Offset, child.Offset,
					"block count exceeds limit %d", r.limits.MaxBlocks)
			}
		case IDBlockGroup:
			if err := r.ParseTree(child); err != nil {
				return 0, err
			}
			if blockEl := FindFirst(child, IDBlock); blockEl != nil {
				b, derr := r.parseBlock(blockEl, cluster.Index, len(doc.Blocks))
				if derr != nil {
					return 0, derr
				}
				b.ClusterIndex = cluster.Index
				cluster.Blocks = append(cluster.Blocks, b)
				doc.Blocks = append(doc.Blocks, b)
			}
		}
		off = child.End
	}
	if !e.Known {
		e.End = end
	}
	return end, nil
}

func (r *Reader) parseBlock(e *Element, clusterIndex, globalIndex int) (*Block, *report.Error) {
	if !e.Known {
		return nil, report.NewError(report.EBMLIllegalLength, "unknown_block_length", HexID(e.ID), e.Offset, e.SizeOffset,
			"block has unknown length")
	}
	p := e.PayloadOffset
	tv, err := r.VINTAt(p, 4)
	if err != nil {
		err.Record.ElementID = HexID(e.ID)
		err.Record.InputOffset = e.Offset
		return nil, err
	}
	if tv.Offset+tv.Width+3 > e.End {
		rec := report.NewError(report.EBMLIncompleteBlock, "incomplete_block", HexID(e.ID), e.Offset, e.End,
			"block header needs track VINT plus timecode and flags; element ends at %d", e.End)
		return nil, rec
	}
	timeBits := binary.BigEndian.Uint16(r.data[p+tv.Width : p+tv.Width+2])
	flags := r.data[p+tv.Width+2]
	frameStart := p + tv.Width + 3
	b := &Block{
		Index:           globalIndex,
		ClusterIndex:    clusterIndex,
		Element:         e,
		Track:           tv.Value,
		TrackVINTOffset: p,
		RelativeTicks:   int16(timeBits),
		Flags:           flags,
		Keyframe:        flags&0x80 != 0,
		FrameLength:     e.End - frameStart,
	}
	if b.FrameLength < 0 {
		return nil, report.NewError(report.EBMLIncompleteBlock, "incomplete_block", HexID(e.ID), e.Offset, e.End,
			"block has no frame bytes")
	}
	return b, nil
}
