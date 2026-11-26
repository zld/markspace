package ebml

import (
	"bytes"
	"encoding/binary"
	"io"
)

// Track describes one Matroska track entry.
type Track struct {
	Number   uint64
	Type     uint64 // Matroska TrackType: 1=video 2=audio 17=subtitle
	CodecID  string
	Rate     float64
	Channels uint64
	IsAudio  bool
}

// Block is one indexed media block. Payloads stay on disk; only offsets and
// timestamps are retained.
type Block struct {
	TrackNo    uint64
	TimeNs     int64 // cluster timecode + relative block timecode, scaled to ns
	IsKeyframe bool
	ElemOffset int64 // offset of the block element's ID byte
	DataOffset int64 // offset of the frame bytes (after the block header)
	DataSize   int64
}

// Document holds the bounded metadata recovered from the container header.
type Document struct {
	TimecodeScale uint64
	MuxingApp     string
	WritingApp    string
	DocType       string
	Tracks        []Track
	SegmentOffset int64
	BlockCount    int
}

// Scan streams over r. Every block is passed to emit as it is found; the
// caller decides which tracks to keep. Memory use is independent of input
// size (header reads are capped by Limits).
func Scan(r io.ReaderAt, size int64, lim Limits, emit func(Block) error) (*Document, error) {
	if size > lim.MaxFileBytes {
		return nil, &Error{ErrLimitExceeded, 0, "input exceeds size limit"}
	}
	doc := &Document{TimecodeScale: 1_000_000}
	hdr, err := ReadElementHeader(io.NewSectionReader(r, 0, size), 0)
	if err != nil {
		return nil, err
	}
	if hdr.ID != IDEBML {
		return nil, &Error{ErrInvalidEBML, 0, "missing EBML header"}
	}
	if err := walkChildren(r, hdr, func(e Element) error {
		if e.ID == IDEBMLDocType {
			v, err := ReadString(r, e)
			if err == nil {
				doc.DocType = v
			}
			return err
		}
		return nil
	}); err != nil {
		return nil, err
	}
	segOff := hdr.End
	if segOff >= size {
		return nil, &Error{ErrTruncatedElem, segOff, "missing Segment element"}
	}
	seg, err := ReadElementHeader(io.NewSectionReader(r, segOff, size-segOff), segOff)
	if err != nil {
		return nil, err
	}
	if seg.ID != IDSegment {
		return nil, &Error{ErrInvalidEBML, segOff, "missing Segment element"}
	}
	doc.SegmentOffset = seg.Offset
	segEnd := seg.End
	if segEnd < 0 {
		segEnd = size // unknown-length Segment runs to EOF
	}
	pos := seg.DataOff
	for pos < segEnd {
		el, err := ReadElementHeader(io.NewSectionReader(r, pos, segEnd-pos), pos)
		if err != nil {
			return nil, err
		}
		next, err := el.Next(segEnd)
		if err != nil {
			return nil, err
		}
		switch el.ID {
		case IDInfo:
			if err := readInfo(r, el, doc); err != nil {
				return nil, err
			}
		case IDTracks:
			if err := readTracks(r, el, lim, doc); err != nil {
				return nil, err
			}
		case IDCluster:
			if err := scanCluster(r, el, segEnd, doc, lim, emit); err != nil {
				return nil, err
			}
		}
		pos = next
	}
	return doc, nil
}

func readHeader(r io.ReaderAt, hdr Element, doc *Document) error {
	return walkChildren(r, hdr, func(e Element) error {
		if e.ID == IDEBMLDocType {
			v, err := ReadString(r, e)
			doc.DocType = v
			return err
		}
		return nil
	})
}

func readInfo(r io.ReaderAt, info Element, doc *Document) error {
	return walkChildren(r, info, func(e Element) error {
		switch e.ID {
		case IDTimecodeScale:
			v, err := ReadUint(r, e)
			if err == nil {
				doc.TimecodeScale = v
			}
			return err
		case IDMuxingApp:
			v, err := ReadString(r, e)
			if err == nil {
				doc.MuxingApp = v
			}
			return err
		case IDWritingApp:
			v, err := ReadString(r, e)
			if err == nil {
				doc.WritingApp = v
			}
			return err
		}
		return nil
	})
}

func readTracks(r io.ReaderAt, tracks Element, lim Limits, doc *Document) error {
	return walkChildren(r, tracks, func(e Element) error {
		if e.ID != IDTrackEntry {
			return nil
		}
		if len(doc.Tracks) >= lim.MaxTracks {
			return &Error{ErrLimitExceeded, e.Offset, "track count exceeds limit"}
		}
		t := Track{Rate: 8000, Channels: 1}
		err := walkChildren(r, e, func(te Element) error {
			switch te.ID {
			case IDTrackNumber:
				v, err := ReadUint(r, te)
				if err == nil {
					t.Number = v
				}
				return err
			case IDTrackType:
				v, err := ReadUint(r, te)
				if err == nil {
					t.Type = v
				}
				return err
			case IDCodecID:
				v, err := ReadString(r, te)
				if err == nil {
					t.CodecID = v
				}
				return err
			case IDAudio:
				t.IsAudio = true
				return walkChildren(r, te, func(a Element) error {
					switch a.ID {
					case IDSamplingFreq:
						v, err := ReadFloat(r, a)
						if err == nil {
							t.Rate = v
						}
						return err
					case IDChannels:
						v, err := ReadUint(r, a)
						if err == nil {
							t.Channels = v
						}
						return err
					}
					return nil
				})
			}
			return nil
		})
		if err != nil {
			return err
		}
		doc.Tracks = append(doc.Tracks, t)
		return nil
	})
}

func scanCluster(r io.ReaderAt, cluster Element, bound int64, doc *Document, lim Limits, emit func(Block) error) error {
	var clusterTC int64
	end := cluster.End
	if end < 0 {
		end = bound // unknown-length Cluster ends with the parent
	}
	return walkChildrenAt(r, cluster.DataOff, end, func(e Element) error {
		switch e.ID {
		case IDTimecode:
			v, err := ReadUint(r, e)
			if err != nil {
				return err
			}
			clusterTC = int64(v)
		case IDSimpleBlock:
			b, err := parseBlock(r, e, clusterTC, int64(doc.TimecodeScale), true)
			if err != nil {
				return err
			}
			doc.BlockCount++
			if doc.BlockCount > lim.MaxBlocks {
				return &Error{ErrLimitExceeded, e.Offset, "block count exceeds limit"}
			}
			return emit(b)
		case IDBlockGroup:
			referenced := false
			return walkChildren(r, e, func(bg Element) error {
				switch bg.ID {
				case IDReferenceBlock:
					referenced = true
				case IDBlock:
					b, err := parseBlock(r, bg, clusterTC, int64(doc.TimecodeScale), false)
					if err != nil {
						return err
					}
					b.IsKeyframe = !referenced
					doc.BlockCount++
					if doc.BlockCount > lim.MaxBlocks {
						return &Error{ErrLimitExceeded, bg.Offset, "block count exceeds limit"}
					}
					return emit(b)
				}
				return nil
			})
		}
		return nil
	})
}

func parseBlock(r io.ReaderAt, e Element, clusterTC, scale int64, simple bool) (Block, error) {
	if e.Size < 4 {
		return Block{}, &Error{ErrInvalidEBML, e.Offset, "block payload too short"}
	}
	head := make([]byte, e.Size)
	if _, err := io.ReadFull(io.NewSectionReader(r, e.DataOff, e.Size), head); err != nil {
		return Block{}, &Error{ErrTruncatedElem, e.DataOff, "block payload truncated"}
	}
	trackRaw, tw, _, err := ReadVINT(bytes.NewReader(head), 0)
	if err != nil {
		return Block{}, &Error{ErrInvalidEBML, e.DataOff, "bad block track VINT"}
	}
	trackNo := VINTData(trackRaw, tw)
	if int64(tw)+2 >= e.Size {
		return Block{}, &Error{ErrInvalidEBML, e.DataOff, "block header truncated"}
	}
	rel := int16(binary.BigEndian.Uint16(head[tw : tw+2]))
	flags := head[tw+2]
	if simple && flags&0x06 != 0 {
		return Block{}, &Error{ErrInvalidEBML, e.DataOff, "laced blocks are not supported"}
	}
	hdrLen := int64(tw) + 3
	return Block{
		TrackNo:    trackNo,
		TimeNs:     (clusterTC + int64(rel)) * scale,
		IsKeyframe: flags&0x80 != 0,
		ElemOffset: e.Offset,
		DataOffset: e.DataOff + hdrLen,
		DataSize:   e.Size - hdrLen,
	}, nil
}
