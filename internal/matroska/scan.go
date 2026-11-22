package matroska

import (
	"encoding/binary"
	"io"

	"mediacheck/internal/ebml"
)

// ClusterScan delivers blocks of one cluster plus its absolute timecode.
type ClusterScan struct {
	TimecodeTicks int64
	Blocks        []Block
}

// ScanClusters streams every cluster inside seg. hardEnd is the segment's
// known byte boundary (file size for unknown-length segments). A defect stops
// the whole material: the returned error carries offset, class and track.
func (p *Parser) ScanClusters(seg *ebml.Element, hardEnd int64, emit func(ClusterScan) error) error {
	// Segment-level iteration uses the hard boundary only; the sibling-ID
	// stop lives one level down, inside unknown-length clusters.
	it := p.r.Children(seg, hardEnd)
	for {
		e, err := it.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return wrapErr(err, 0)
		}
		if e.ID != IDCluster {
			if err := p.r.SkipElement(e); err != nil {
				return wrapErr(err, 0)
			}
			continue
		}
		// An unknown-length cluster is bounded by the next cluster sibling or
		// the segment's hard end.
		clusterEnd := hardEnd
		if !e.UnknownSize {
			clusterEnd = e.EndOff
		}
		if err := p.scanCluster(e, clusterEnd, emit); err != nil {
			return err
		}
	}
}

func (p *Parser) scanCluster(cluster *ebml.Element, hardEnd int64, emit func(ClusterScan) error) error {
	var clusterTC int64
	haveTC := false
	var blocks []Block
	// Unknown-length clusters end at the next Cluster sibling or segment end.
	it := p.r.Children(cluster, hardEnd, IDCluster)
	for {
		e, err := it.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return wrapErr(err, 0)
		}
		switch e.ID {
		case IDTimecode:
			v, err := p.r.ReadUint(int(e.Size))
			if err != nil {
				return wrapErr(err, 0)
			}
			clusterTC = int64(v)
			haveTC = true
		case IDSimpleBlock, IDBlock:
			b, err := p.scanBlock(e, clusterTC, haveTC)
			if err != nil {
				return err
			}
			blocks = append(blocks, b)
		default:
			if err := p.r.SkipElement(e); err != nil {
				return wrapErr(err, 0)
			}
		}
	}
	if !haveTC {
		return &ParseError{Class: ebml.ErrBadElement, Offset: cluster.HeaderOff, Msg: "cluster without Timecode"}
	}
	return emit(ClusterScan{TimecodeTicks: clusterTC, Blocks: blocks})
}

// scanBlock parses the Matroska Block header (track VINT, 16-bit signed
// relative timecode, flags) and validates payload bounds. Payload bytes are
// discarded, never decoded.
func (p *Parser) scanBlock(e *ebml.Element, clusterTC int64, haveTC bool) (Block, error) {
	startOff := p.r.Offset()
	track, tw, _, err := p.r.ReadVInt(false)
	if err != nil {
		return Block{}, wrapErr(err, 0)
	}
	spec, known := p.tracks[track]
	if !known {
		return Block{}, &ParseError{
			Class: ebml.ErrBadElement, Offset: startOff, TrackNumber: track,
			Msg: "block references undeclared track",
		}
	}
	tcBytes, err := p.r.ReadBytes(2)
	if err != nil {
		return Block{}, wrapErr(err, track)
	}
	rel := int16(binary.BigEndian.Uint16(tcBytes))
	flags, err := p.r.ReadByteRaw()
	if err != nil {
		return Block{}, wrapErr(err, track)
	}
	headerLen := int64(tw) + 3
	headerEnd := startOff + headerLen
	if e.UnknownSize {
		return Block{}, &ParseError{Class: ebml.ErrBadElement, Offset: e.HeaderOff, TrackNumber: track, Msg: "block has unknown length"}
	}
	end := e.EndOff
	if end < headerEnd {
		return Block{}, &ParseError{
			Class: ebml.ErrTruncated, Offset: end, TrackNumber: track,
			Msg: "block payload truncated: header exceeds declared element size",
		}
	}
	// Discard payload through the declared end; a short read is TRUNCATED.
	if _, err := p.r.Discard(end - p.r.Offset()); err != nil {
		return Block{}, wrapErr(err, track)
	}
	abs := clusterTC + int64(rel)
	if haveTC && rel != 0 {
		// Relative timecode outside a sane cluster window is a structural fault:
		// Matroska requires block timecodes to stay within the parent cluster.
		if int64(rel) < 0 || (spec.DefaultDur > 0 && int64(rel) > int64(spec.DefaultDur)) {
			return Block{}, &ParseError{
				Class: ebml.ErrBadElement, Offset: startOff, TrackNumber: track,
				Msg: "block relative timecode outside parent cluster range",
			}
		}
	}
	return Block{
		TrackNumber:  track,
		RelTimecode:  rel,
		Keyframe:     e.ID == IDSimpleBlock && flags&0x80 != 0,
		AbsTimeTicks: abs,
		HeaderOffset: e.HeaderOff,
		PayloadSize:  uint64(end - headerEnd),
	}, nil
}
