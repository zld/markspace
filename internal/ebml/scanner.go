package ebml

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"mediaarchive/internal/timeline"
)

// Parse walks the EBML header and Segment, recovering as many block events
// as possible. Structural failures are returned both as collected issues and
// as the final non-nil error when nothing more can be scanned.
func (r *Reader) Parse() (*ParseResult, error) {
	res := &ParseResult{Report: r.report}
	hdr, err := r.readElementHeader()
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return res, r.fail("parse_header", r.pos(), timeline.RecoverNone,
				timeline.NewError("ebml", "parse_header", r.pos(),
					timeline.RecoverNone, "EBML header truncated",
					timeline.ErrTruncated))
		}
		return res, r.fail("parse_header", 0, timeline.RecoverNone, err)
	}
	if !bytes.Equal(hdr.ID, IDEBML) {
		return res, r.fail("parse_header", hdr.Offset, timeline.RecoverNone,
			timeline.NewError("ebml", "header", hdr.Offset, timeline.RecoverNone,
				fmt.Sprintf("first element % x is not EBML header", hdr.ID),
				timeline.ErrOutOfBounds))
	}
	if err := r.skipPayload(hdr); err != nil {
		return res, r.fail("parse_header", hdr.Offset, timeline.RecoverNone, err)
	}
	seg, err := r.readElementHeader()
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return res, r.fail("parse_segment", r.pos(), timeline.RecoverNone,
				timeline.NewError("ebml", "parse_segment", r.pos(),
					timeline.RecoverNone, "segment header truncated",
					timeline.ErrTruncated))
		}
		return res, r.fail("parse_segment", r.pos(), timeline.RecoverNone, err)
	}
	if !bytes.Equal(seg.ID, IDSegment) {
		return res, r.fail("parse_segment", seg.Offset, timeline.RecoverNone,
			timeline.NewError("ebml", "segment", seg.Offset, timeline.RecoverNone,
				fmt.Sprintf("expected Segment, got % x", seg.ID),
				timeline.ErrOutOfBounds))
	}
	segEnd := seg.End
	if segEnd < 0 {
		segEnd = r.size // unknown-length segment runs to end of file
	}
	if seg.Truncated {
		r.issue("parse_segment", seg.Offset, timeline.RecoverPartial,
			"segment declared size exceeds input; clamped to EOF",
			timeline.ErrTruncated)
	}
	res.Segment = seg
	events, err := r.parseChildren(segEnd, seg.Size < 0)
	res.Events = events
	if err != nil && errors.Is(err, io.EOF) {
		err = nil // truncated tail already recorded as an issue
	}
	return res, err
}

// frame is one open container on the ancestor stack.
type frame struct {
	end     int64 // hard boundary (known end or file/segment end)
	unknown bool
}

// parseChildren scans elements until the parent boundary or EOF. Unknown
// length containers are closed implicitly when the next sibling element
// begins, per EBML's "unknown-size ends at the next element" rule.
func (r *Reader) parseChildren(parentEnd int64, parentUnknown bool) ([]BlockEvent, error) {
	var events []BlockEvent
	stack := []frame{{end: parentEnd, unknown: parentUnknown}}
	for len(stack) > 0 {
		top := stack[len(stack)-1]
		pos := r.pos()
		if pos >= top.end {
			stack = stack[:len(stack)-1]
			continue
		}
		el, err := r.readElementHeader()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				// A truncated header inside a cluster marks that cluster.
				if len(stack) > 1 {
					r.report.TruncatedClusterOffsets = append(
						r.report.TruncatedClusterOffsets, pos)
				}
				r.issue("scan_children", pos, timeline.RecoverPartial,
					"truncated element header", timeline.ErrTruncated)
				return events, io.EOF
			}
			return events, err
		}
		end := el.End
		unknownChild := el.Size < 0
		if end < 0 {
			end = top.end
		}
		if end > top.end {
			// Declared element crosses its container: truncation.
			if len(stack) > 1 {
				r.report.TruncatedClusterOffsets = append(
					r.report.TruncatedClusterOffsets, el.Offset)
			}
			r.issue("scan_children", el.Offset, timeline.RecoverPartial,
				"element crosses container boundary", timeline.ErrTruncated)
			return events, io.EOF
		}
		switch {
		case bytes.Equal(el.ID, IDCluster):
			ev, err := r.parseClusterFrame(el, end, unknownChild)
			events = append(events, ev...)
			if err != nil {
				return events, err
			}
		case bytes.Equal(el.ID, IDSimpleBlk), bytes.Equal(el.ID, IDBlock):
			ev, err := r.parseBlock(el, end, 0)
			if err != nil {
				return events, err
			}
			events = append(events, ev)
		case isContainer(el.ID):
			stack = append(stack, frame{end: end, unknown: unknownChild})
		default:
			if err := r.skipPayload(el); err != nil {
				return events, r.fail("skip_payload", el.Offset,
					timeline.RecoverPartial, err)
			}
		}
	}
	return events, nil
}

// parseClusterFrame parses one cluster. Known-length clusters own their
// span; unknown-length clusters consume children until the next cluster or
// the segment boundary.
func (r *Reader) parseClusterFrame(cl Element, end int64, unknown bool) ([]BlockEvent, error) {
	var events []BlockEvent
	var clusterTimecode int64
	clusterSeen := false
	for r.pos() < end {
		pos := r.pos()
		el, err := r.readElementHeader()
		if err != nil {
			r.report.TruncatedClusterOffsets = append(
				r.report.TruncatedClusterOffsets, cl.Offset)
			r.issue("parse_cluster", pos, timeline.RecoverPartial,
				"truncated cluster", timeline.ErrTruncated)
			return events, io.EOF
		}
		// An unknown-length cluster ends at the next sibling cluster.
		if unknown && bytes.Equal(el.ID, IDCluster) {
			if err := r.seek(el.Offset); err != nil {
				return events, err
			}
			return events, nil
		}
		childEnd := el.End
		if childEnd < 0 {
			childEnd = end
		}
		if childEnd > end {
			r.report.TruncatedClusterOffsets = append(
				r.report.TruncatedClusterOffsets, cl.Offset)
			r.issue("parse_cluster", el.Offset, timeline.RecoverPartial,
				"cluster child crosses boundary", timeline.ErrTruncated)
			return events, io.EOF
		}
		switch {
		case bytes.Equal(el.ID, IDTimecode):
			raw, err := r.readUintPayload(el, childEnd)
			if err != nil {
				r.report.TruncatedClusterOffsets = append(
					r.report.TruncatedClusterOffsets, cl.Offset)
				return events, io.EOF
			}
			clusterTimecode = int64(raw)
			clusterSeen = true
		case bytes.Equal(el.ID, IDSimpleBlk), bytes.Equal(el.ID, IDBlock):
			ev, err := r.parseBlock(el, childEnd, clusterTimecode)
			if err != nil {
				if errors.Is(err, timeline.ErrLimitExceeded) ||
					errors.Is(err, timeline.ErrOverflow) {
					return events, err // fatal policy violation, not truncation
				}
				r.report.TruncatedClusterOffsets = append(
					r.report.TruncatedClusterOffsets, cl.Offset)
				return events, io.EOF
			}
			events = append(events, ev)
		case isContainer(el.ID):
			nested, err := r.parseChildren(childEnd, el.Size < 0)
			events = append(events, nested...)
			if err != nil {
				return events, err
			}
		default:
			if err := r.seek(childEnd); err != nil {
				return events, err
			}
		}
	}
	_ = clusterSeen
	// A known-length cluster that promised more bytes than the file holds
	// has been clamped to EOF; its complete blocks are kept but the
	// truncation is recorded against the cluster offset.
	if !unknown && cl.Truncated {
		r.report.TruncatedClusterOffsets = append(
			r.report.TruncatedClusterOffsets, cl.Offset)
		r.issue("parse_cluster", cl.Offset, timeline.RecoverPartial,
			"cluster declared size exceeds input; intact blocks kept",
			timeline.ErrTruncated)
	}
	return events, nil
}

// parseBlock reads one Block/SimpleBlock header and captures its payload.
func (r *Reader) parseBlock(el Element, end int64, clusterTC int64) (BlockEvent, error) {
	if end-el.PayloadOffset < 4 {
		return BlockEvent{}, timeline.NewError("ebml", "parse_block", el.Offset,
			timeline.RecoverPartial, "block header truncated", timeline.ErrTruncated)
	}
	payloadLen := end - el.PayloadOffset
	if payloadLen > r.limits.MaxBlockSize {
		r.report.OversizedElements = append(r.report.OversizedElements, el.Offset)
		return BlockEvent{}, timeline.NewError("ebml", "parse_block", el.Offset,
			timeline.RecoverNone, "block exceeds size cap", timeline.ErrLimitExceeded)
	}
	buf := make([]byte, payloadLen)
	if _, err := io.ReadFull(r.r, buf); err != nil {
		return BlockEvent{}, timeline.NewError("ebml", "parse_block", el.Offset,
			timeline.RecoverPartial, "block payload truncated", timeline.ErrTruncated)
	}
	track, n1, err := readVINT(buf)
	if err != nil {
		return BlockEvent{}, timeline.NewError("ebml", "parse_block", el.Offset,
			timeline.RecoverPartial, "bad track VINT", err)
	}
	if int(n1)+3 > len(buf) {
		return BlockEvent{}, timeline.NewError("ebml", "parse_block", el.Offset,
			timeline.RecoverPartial, "block header truncated", timeline.ErrTruncated)
	}
	relTC := int16(binary.BigEndian.Uint16(buf[n1 : n1+2]))
	flags := buf[n1+2]
	absTC, err := timeline.Advance(clusterTC, relTC)
	if err != nil {
		return BlockEvent{}, err
	}
	tb := timeline.TimeBase{ClockRate: 1000} // Matroska timecodes are milliseconds
	nanos, err := tb.ToNanos(uint64(absTC))
	if err != nil {
		return BlockEvent{}, err
	}
	r.seq++
	ev := BlockEvent{
		Track: uint32(track), TimeNanos: nanos, Flags: flags,
		Keyframe:  flags&0x80 != 0,
		Payload:    buf[n1+3:],
		SourceSeq:  r.seq,
		FileOffset: el.Offset,
	}
	r.report.BlockCount++
	if r.report.FirstBlockNanos < 0 {
		r.report.FirstBlockNanos = nanos
	}
	r.report.LastBlockNanos = nanos
	return ev, nil
}
