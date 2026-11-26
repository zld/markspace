package ebml

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"pwflow/internal/fault"
)

func (p *parser) parseCluster(h Header, end int64) (int64, error) {
	if h.Unknown {
		p.rep.UnknownClusters++
	}
	off := h.DataOffset
	clusterTC := int64(0)
	for off < end {
		ch, err := ReadHeader(p.data, off)
		if err != nil {
			return p.recoverAt(off, fault.EBMLHeader, err.Error())
		}
		if h.Unknown && (ch.ID == IDCluster || isTopLevelSibling(ch.ID)) {
			return off, nil
		}
		bodyEnd, ok := safeEnd(ch, end)
		if !ok {
			return p.recoverAt(off, fault.EBMLVINT, "child element exceeds Cluster bounds")
		}
		switch ch.ID {
		case IDTimecode:
			if bodyEnd-ch.DataOffset > 8 {
				return p.recoverAt(off, fault.EBMLVINT, "Cluster Timecode too large")
			}
			clusterTC = int64(readUint(p.data[ch.DataOffset:bodyEnd]))
		case IDSimpleBlock:
			if err := p.parseBlock(ch, bodyEnd, clusterTC, true); err != nil {
				return p.recoverAt(off, fault.EBMLBadBlock, err.Error())
			}
		case IDBlockGroup:
			blockOff, blockH, blockEnd, found, berr := findBlock(p.data, ch.DataOffset, bodyEnd)
			if berr != nil {
				return p.recoverAt(off, fault.EBMLBadBlock, berr.Error())
			}
			if found {
				if err := p.parseBlockAt(blockOff, blockH, blockEnd, clusterTC, false); err != nil {
					return p.recoverAt(blockOff, fault.EBMLBadBlock, err.Error())
				}
			}
		}
		off = bodyEnd
	}
	return end, nil
}

func (p *parser) recoverAt(badOff int64, code fault.Code, msg string) (int64, error) {
	d, err := p.resyncCluster(badOff, code, msg)
	if err != nil {
		return badOff, err
	}
	p.rep.Damages = append(p.rep.Damages, d)
	return d.SkippedTo, nil
}

func findBlock(data []byte, start, end int64) (int64, Header, int64, bool, error) {
	var found Header
	var foundOff, foundEnd int64
	ok := false
	err := eachChild(data, start, end, func(h Header, bodyEnd int64) error {
		if h.ID != IDBlock {
			return nil
		}
		if ok {
			return fmt.Errorf("multiple Blocks in BlockGroup")
		}
		found = h
		foundOff = start // replaced below by element offset unavailable in callback
		foundEnd = bodyEnd
		ok = true
		return nil
	})
	_ = foundOff
	// eachChild's callback receives only the header; calculate the element
	// offset from DataOffset and HeaderSize for the returned anchor.
	if ok {
		foundOff = found.DataOffset - int64(found.HeaderSize)
	}
	return foundOff, found, foundEnd, ok, err
}

func (p *parser) parseBlock(h Header, bodyEnd, clusterTC int64, simple bool) error {
	return p.parseBlockAt(h.DataOffset-int64(h.HeaderSize), h, bodyEnd, clusterTC, simple)
}

func (p *parser) parseBlockAt(elementOff int64, h Header, bodyEnd, clusterTC int64, simple bool) error {
	start := h.DataOffset
	payloadLen := bodyEnd - start
	track, width, _, err := readVINT(p.data, int(start), false)
	if err != nil {
		return fault.At(fault.EBMLBadBlock, elementOff, "track VINT: %v", err)
	}
	relOff := start + int64(width)
	if payloadLen < int64(width)+3 || relOff+3 > bodyEnd {
		return fault.At(fault.EBMLBadBlock, elementOff, "truncated block header")
	}
	rel := int16(binary.BigEndian.Uint16(p.data[relOff : relOff+2]))
	flags := p.data[relOff+2]
	scale := p.rep.TimecodeScaleNS
	if scale <= 0 {
		scale = 1_000_000
	}
	pts := (clusterTC + int64(rel)) * scale
	b := Block{
		ElementOffset:      elementOff,
		DataOffset:         start,
		TrackNumber:        track,
		ClusterTimecodeNS:  clusterTC * scale,
		RelativeTimecodeMS: rel,
		PTSNS:              pts,
		Simple:             simple,
		PayloadBytes:       int(payloadLen),
	}
	if simple {
		b.Keyframe = flags&0x80 != 0
	}
	p.rep.Blocks = append(p.rep.Blocks, b)
	return nil
}

func (p *parser) resyncCluster(badOff int64, code fault.Code, msg string) (Damage, error) {
	limit := p.limits.MaxResyncBytes
	if limit <= 0 {
		limit = DefaultMaxResyncBytes
	}
	scanEnd := badOff + limit
	if scanEnd > int64(len(p.data)) {
		scanEnd = int64(len(p.data))
	}
	magic := EncodeID(IDCluster)
	from := badOff
	if from < 0 {
		from = 0
	}
	for from < scanEnd {
		idx := bytes.Index(p.data[from:scanEnd], magic)
		if idx < 0 {
			break
		}
		at := from + int64(idx)
		if h, err := ReadHeader(p.data, at); err == nil && h.ID == IDCluster {
			if end, ok := safeEnd(h, int64(len(p.data))); ok && p.confirmsCluster(h, end) {
				return Damage{
					Record: fault.Record{
						Code:    code,
						Offset:  badOff,
						Message: fmt.Sprintf("%s; skipped untrusted range [%d,%d)", msg, badOff, at),
					},
					SkippedTo: at,
				}, nil
			}
		}
		from = at + 1
	}
	return Damage{}, fault.At(fault.EBMLResyncFailed, badOff,
		"%s; no next Cluster within %d-byte resync window", msg, limit)
}

func (p *parser) confirmsCluster(h Header, end int64) bool {
	if h.DataOffset+2 > end {
		return false
	}
	tc, err := ReadHeader(p.data, h.DataOffset)
	if err != nil || tc.ID != IDTimecode {
		return false
	}
	tcEnd, ok := safeEnd(tc, end)
	if !ok || tc.Unknown || tc.Size > 8 {
		return false
	}
	_ = readUint(p.data[tc.DataOffset:tcEnd])
	return true
}

func isTopLevelSibling(id uint32) bool {
	switch id {
	case IDInfo, IDTracks, IDSeekHead, IDCues, IDTags, IDChapters, IDAttachments, IDSegment:
		return true
	default:
		return false
	}
}
