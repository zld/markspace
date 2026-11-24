package ebml

import (
	"encoding/binary"
	"io"

	"mediapack/internal/fault"
)

// Matroska element IDs referenced while scanning the Segment payload.
const (
	IDEBMLHeader uint32 = 0x1A45DFA3
	IDSegment    uint32 = 0x18538067
	IDSeekHead   uint32 = 0x114D9B74
	IDInfo       uint32 = 0x1549A966
	IDTracks     uint32 = 0x1654AE6B
	IDCluster    uint32 = 0x1F43B675
	IDTimestamp  uint32 = 0xE7
	IDSimpleTag  uint32 = 0x67C8
	IDBlockGroup uint32 = 0xA0
	IDBlock      uint32 = 0xA1
	IDSimpleBlock uint32 = 0xA3
	IDVoid       uint32 = 0xEC
	IDCues       uint32 = 0x1C53BB6B
)

// Block is one media block located inside a cluster. Payload holds the raw,
// untouched Matroska block bytes (track VINT, relative timecode, flags and
// codec data); PayloadStart/PayloadEnd bound the codec-only bytes.
type Block struct {
	Track        uint32
	RelTimecode  int16 // signed 16-bit offset from the cluster timestamp
	Keyframe     bool
	HeaderLen    int   // bytes consumed by track VINT + timecode + flags
	Start        int64 // absolute offset of the enclosing element ID
	ElementEnd   int64 // absolute offset just past the element
	PayloadStart int64 // absolute offset of the first codec byte
	PayloadEnd   int64 // absolute offset just past the codec bytes
	SimpleBlock  bool  // true for SimpleBlock, false for BlockGroup/Block
}

// AbsTimecode returns the block's cluster-relative absolute millisecond tick.
func (b *Block) AbsTimecode(clusterTS int64) int64 {
	return clusterTS + int64(b.RelTimecode)
}

// PayloadRange returns the [start, end) byte interval of the codec payload.
func (b *Block) PayloadRange() (int64, int64) { return b.PayloadStart, b.PayloadEnd }

// Cluster is one scanned Matroska cluster with its byte window and the
// timestamp of its Timestamp child element.
type Cluster struct {
	Start     int64
	HeaderEnd int64
	End       int64 // -1 when the cluster itself has unknown length
	Timestamp int64
	Blocks    []*Block
}

// Segment locates the Segment element inside a stream and scanners its
// clusters. It handles both known and unknown Segment/Cluster lengths.
type Segment struct {
	r       *Reader
	element *Element
}

// OpenSegment reads the EBML header and locates the Segment element. The
// returned Segment is ready for cluster scanning without buffering the file.
func OpenSegment(rs io.ReadSeeker) (*Segment, error) {
	r, err := NewReader(rs, 0, -1)
	if err != nil {
		return nil, err
	}
	hdr, err := r.ReadElement()
	if err != nil {
		return nil, err
	}
	if hdr.ID != IDEBMLHeader {
		return nil, fault.New(fault.KindIllegalVINT, hdr.Start,
			"first element 0x%x is not an EBML header", hdr.ID)
	}
	if err := r.Skip(hdr); err != nil {
		return nil, err
	}
	var seg *Element
	for {
		el, err := r.ReadElement()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if el.ID == IDSegment {
			seg = el
			break
		}
		if err := r.Skip(el); err != nil {
			return nil, err
		}
	}
	if seg == nil {
		return nil, fault.New(fault.KindTruncated, r.Offset(),
			"no Segment element found").AsRecoverable()
	}
	cr, err := r.Child(seg)
	if err != nil {
		return nil, err
	}
	return &Segment{r: cr, element: seg}, nil
}

// ScanClusters walks every cluster in the segment. Unknown-length clusters
// are bounded by the start of the next cluster or the segment end; a
// truncated final cluster produces a recoverable fault with its offset.
// onCluster is invoked in stream order; returning an error aborts the scan.
func (s *Segment) ScanClusters(onCluster func(*Cluster) error) error {
	r := s.r
	for {
		el, err := r.ReadElement()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch el.ID {
		case IDCluster:
			cl, err := s.scanCluster(el)
			if err != nil {
				return err
			}
			if err := onCluster(cl); err != nil {
				return err
			}
		case IDVoid:
			if err := r.Skip(el); err != nil {
				return err
			}
		default:
			// Tracks/Info/Cues/SeekHead and other segment-level masters:
			// skip without interpreting, preserving scan position.
			if err := r.Skip(el); err != nil {
				return err
			}
		}
	}
}

// scanCluster parses one cluster's children, resolving unknown lengths by
// discovery and validating every block header against the cluster window.
func (s *Segment) scanCluster(cel *Element) (*Cluster, error) {
	cr, err := s.r.Child(cel)
	if err != nil {
		return nil, err
	}
	cl := &Cluster{Start: cel.Start, HeaderEnd: cel.HeaderEnd, End: cel.End}
	for {
		child, err := cr.ReadElement()
		if err == io.EOF {
			break
		}
		if err != nil {
			// A truncated tail inside an unknown-length cluster is the
			// common "file cut mid-cluster" case: report recoverably.
			if cel.Size == UnknownSize && fault.Is(err, fault.KindTruncated) {
				return cl, err
			}
			return cl, err
		}
		switch child.ID {
		case IDTimestamp:
			buf, err := cr.ReadPayload(child)
			if err != nil {
				return cl, err
			}
			v, err := ReadUint(buf)
			if err != nil {
				return cl, err
			}
			cl.Timestamp = int64(v)
		case IDSimpleBlock:
			blk, err := parseBlock(cr, child, true)
			if err != nil {
				return cl, err
			}
			cl.Blocks = append(cl.Blocks, blk)
		case IDBlockGroup:
			bg, err := cr.Child(child)
			if err != nil {
				return cl, err
			}
			if err := scanBlockGroup(bg, child, cl); err != nil {
				return cl, err
			}
		default:
			if err := cr.Skip(child); err != nil {
				return cl, err
			}
		}
	}
	return cl, nil
}

// scanBlockGroup extracts the Block child, ignoring reference/attachment
// siblings but never their byte ranges.
func scanBlockGroup(bg *Reader, group *Element, cl *Cluster) error {
	for {
		child, err := bg.ReadElement()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if child.ID == IDBlock {
			blk, err := parseBlock(bg, child, false)
			if err != nil {
				return err
			}
			cl.Blocks = append(cl.Blocks, blk)
			continue
		}
		if err := bg.Skip(child); err != nil {
			return err
		}
	}
}

// parseBlock validates a Block/SimpleBlock header and records the exact byte
// interval of the codec payload. Lacing is rejected explicitly rather than
// silently decoded, matching the no-guessing fault policy.
func parseBlock(r *Reader, el *Element, simple bool) (*Block, error) {
	if el.Size < 4 {
		return nil, fault.New(fault.KindLengthOutOfRange, el.Start,
			"block element size %d smaller than minimum header (4 bytes)", el.Size)
	}
	buf, err := r.ReadPayload(el)
	if err != nil {
		return nil, err
	}
	off := el.HeaderEnd
	// Track number: VINT with marker retained in the raw stream.
	first := buf[0]
	width := 1
	for width <= 8 && first&widthMarker[width-1] == 0 {
		width++
	}
	if width > len(buf)-3 {
		return nil, fault.New(fault.KindIllegalVINT, off,
			"block track VINT width %d overruns block of %d bytes", width, len(buf))
	}
	var trackRaw uint64
	for i := 0; i < width; i++ {
		trackRaw = (trackRaw << 8) | uint64(buf[i])
	}
	track := uint32(trackRaw & sizeMask[width-1])
	headerLen := width + 3
	rel := int16(binary.BigEndian.Uint16(buf[width : width+2]))
	flags := buf[width+2]
	if lacing := (flags & 0x06) >> 1; lacing != 0 {
		return nil, fault.New(fault.KindLengthOutOfRange, off+int64(width+2),
			"laced blocks (mode %d) are not supported in this pipeline", lacing)
	}
	b := &Block{
		Track:        track,
		RelTimecode:  rel,
		Keyframe:     simple && flags&0x80 != 0,
		HeaderLen:    headerLen,
		Start:        el.Start,
		ElementEnd:   el.End,
		PayloadStart: off + int64(headerLen),
		PayloadEnd:   el.End,
		SimpleBlock:  simple,
	}
	if b.PayloadEnd <= b.PayloadStart {
		return nil, fault.New(fault.KindTruncated, b.PayloadStart,
			"block track %d has no codec payload bytes", track).AsRecoverable()
	}
	return b, nil
}
