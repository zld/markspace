package ebml

import (
	"encoding/binary"
	"errors"
	"io"
)

func (p *parser) readCluster(h Header) error {
	p.elements++
	clusterOff := h.Offset
	var clusterTC int64
	if h.UnknownSize {
		// Unknown-length cluster: children continue until an element that can
		// only be a sibling or ancestor. Peek before consuming so that header
		// belongs to the parent.
		for {
			id, ok, err := p.rd.PeekID()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return p.wrapErr(h, err)
			}
			if !ok || segmentLevel(id) {
				return nil
			}
			if err := p.readClusterChild(&clusterTC, clusterOff); err != nil {
				return err
			}
		}
	}
	end := h.DataOffset + int64(h.Size)
	for p.rd.Offset() < end {
		if err := p.readClusterChild(&clusterTC, clusterOff); err != nil {
			return err
		}
	}
	if p.rd.Offset() != end {
		return parseError(ErrKindStructure, p.rd.Offset(), h.ID, 0, "cluster payload overran declared size", nil)
	}
	return nil
}

func (p *parser) readClusterChild(clusterTC *int64, clusterOff int64) error {
	child, err := p.rd.Next()
	if err != nil {
		return p.wrapErr(child, err)
	}
	switch child.ID {
	case IDTimestamp:
		buf, err := p.rd.ReadPayload(child, 8)
		if err != nil {
			return p.wrapErr(child, err)
		}
		p.elements++
		*clusterTC = int64(readUint(buf))
		return nil
	case IDSimpleBlock:
		return p.readBlock(child, *clusterTC, clusterOff, true)
	case IDBlockGroup:
		return p.readBlockGroup(child, *clusterTC, clusterOff)
	default:
		return p.discard(child)
	}
}

func (p *parser) readBlockGroup(h Header, clusterTC, clusterOff int64) error {
	if h.UnknownSize {
		return parseError(ErrKindStructure, h.Offset, h.ID, 0, "BlockGroup must have a known size", nil)
	}
	end := h.DataOffset + int64(h.Size)
	p.elements++
	for p.rd.Offset() < end {
		child, err := p.rd.Next()
		if err != nil {
			return p.wrapErr(child, err)
		}
		if child.ID == IDBlock {
			if err := p.readBlock(child, clusterTC, clusterOff, false); err != nil {
				return err
			}
			continue
		}
		if err := p.discard(child); err != nil {
			return err
		}
	}
	return nil
}

// readBlock decodes a block header and streams the payload to the sink. The
// payload is never buffered. SimpleBlock carries the keyframe flag; a Block in
// a BlockGroup does not, so it is reported as non-keyframe and the timeline
// stage derives keyframe-ness from reference structure instead.
func (p *parser) readBlock(h Header, clusterTC, clusterOff int64, simple bool) error {
	if h.UnknownSize {
		return parseError(ErrKindStructure, h.Offset, h.ID, 0, "block must have a known size", nil)
	}
	if h.Size < 4 {
		return parseError(ErrKindTruncated, h.Offset, h.ID, 0, "block shorter than header", nil)
	}
	const maxBlockHeader = 4 + 8
	head := make([]byte, maxBlockHeader)
	n := int64(h.Size)
	if n > maxBlockHeader {
		n = maxBlockHeader
	}
	if err := p.rd.readFull(head[:n]); err != nil {
		return p.wrapErr(h, err)
	}
	track, tw, err := ParseUintVINT(head)
	if err != nil {
		return parseError(ErrKindInvalidVINT, h.Offset, h.ID, 0, "invalid block track number", err)
	}
	if uint64(tw)+3 > h.Size {
		return parseError(ErrKindTruncated, h.Offset, h.ID, track, "block too short for timecode/flags", nil)
	}
	rel := int16(binary.BigEndian.Uint16(head[tw : tw+2]))
	flags := head[tw+2]
	payloadLen := int64(h.Size) - int64(tw) - 3
	payloadOff := h.DataOffset + int64(tw) + 3
	if err := p.rd.skip(int64(h.Size) - n); err != nil {
		return p.wrapErr(h, err)
	}
	b := Block{
		Track:       track,
		Timecode:    clusterTC + int64(rel),
		Keyframe:    simple && flags&0x80 != 0,
		Invisible:   flags&0x08 != 0,
		Discardable: flags&0x01 != 0,
		Source:      h.Span(),
		PayloadOff:  payloadOff,
		PayloadLen:  payloadLen,
		Cluster:     clusterOff,
	}
	p.elements++
	if p.sink == nil {
		return nil
	}
	return p.sink(b)
}
