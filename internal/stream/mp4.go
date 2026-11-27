package stream

import (
	"bytes"
	"fmt"
	"io"

	mp4 "github.com/abema/go-mp4"

	"archivecheck/internal/asset"
	"archivecheck/internal/fault"
)

// maxReportedSpans bounds the number of damaged spans reported for one source.
const maxReportedSpans = 64

// mp4Box is a validated box discovered by the go-mp4 traversal. Offset is the
// absolute byte position of the box header; Payload excludes the header.
type mp4Box struct {
	typ      string
	offset   int64
	payload  int64
	end      int64
	children []*mp4Box
}

// AnalyzeMP4 walks the box tree with go-mp4 and reports truncated or
// out-of-bounds boxes as damaged spans in original byte offsets. A structural
// break ends traversal of its parent because later boundaries cannot be
// trusted; the first break is retained as the parse error.
func AnalyzeMP4(src *asset.Source) *Structure {
	s := &Structure{Source: src.Name, Size: src.Size}
	data := src.Bytes()
	var walker boxWalker
	walker.walk(data, 0, int64(len(data)), true, 0, s)
	if len(s.Damaged) > 0 {
		s.ParseError = s.Damaged[0].Detail
	}
	if len(walker.roots) == 0 {
		return s
	}
	hasMoov := false
	for _, root := range walker.roots {
		if root.typ == "ftyp" {
			if v, ok := readFTYP(data, root); ok {
				s.Brand = v
			}
		}
		if root.typ == "moov" {
			hasMoov = true
			parseMovie(src, root, s)
		}
	}
	if !hasMoov && s.ParseError == "" {
		s.ParseError = "no moov box in the source"
	}
	return s
}

type boxWalker struct {
	roots []*mp4Box
}

func (w *boxWalker) damage(s *Structure, start, end int64, kind, detail string) {
	if len(s.Damaged) >= maxReportedSpans {
		return
	}
	if end <= start {
		end = start + 1
	}
	if end > s.Size {
		end = s.Size
	}
	if start >= s.Size {
		return
	}
	s.Damaged = append(s.Damaged, fault.Span{Start: start, End: end, Kind: kind, Detail: detail})
}

// walk parses the boxes in [start,end) using mp4.ReadBoxInfo. It returns the
// parsed boxes and stops at the first structural break, reporting it.
func (w *boxWalker) walk(data []byte, start, end int64, top bool, depth int, s *Structure) []*mp4Box {
	if depth > 16 {
		w.damage(s, start, start+1, fault.KindLimit, "MP4 box nesting exceeds 16 levels")
		return nil
	}
	var out []*mp4Box
	reader := bytes.NewReader(data[:end])
	pos := start
	for pos < end {
		remaining := end - pos
		if remaining < 8 {
			w.damage(s, pos, end, fault.KindTruncated, fmt.Sprintf("truncated box header: %d bytes remain, need 8", remaining))
			break
		}
		if _, err := reader.Seek(pos, io.SeekStart); err != nil {
			w.damage(s, pos, end, fault.KindCorrupt, "seek failed: "+err.Error())
			break
		}
		info, err := mp4.ReadBoxInfo(reader)
		if err != nil {
			w.damage(s, pos, end, fault.KindCorrupt, "unreadable box header: "+err.Error())
			break
		}
		typ := printable(info.Type.String())
		offset := int64(info.Offset)
		size := int64(info.Size)
		header := int64(info.HeaderSize)
		if info.ExtendToEOF {
			if !top {
				w.damage(s, offset, end, fault.KindInvalid, fmt.Sprintf("box %q inside another box uses the to-EOF size form", typ))
				break
			}
			size = end - offset
			info.Size = uint64(size)
		}
		if size < header {
			w.damage(s, offset, offset+header, fault.KindInvalid,
				fmt.Sprintf("box %q declares size %d below its %d-byte header", typ, size, header))
			break
		}
		if size > remaining {
			w.damage(s, offset, end, fault.KindTruncated,
				fmt.Sprintf("box %q declares size %d but only %d bytes remain", typ, size, remaining))
			break
		}
		box := &mp4Box{typ: typ, offset: offset, payload: offset + header, end: offset + size}
		s.BoxCount++
		if isMP4Container(typ) {
			childStart := box.payload
			if typ == "meta" {
				childStart += 4 // full-box version/flags precede meta children
			}
			// Damage inside the payload does not invalidate this box's own
			// declared boundaries, so the parent walk can still advance.
			box.children = w.walk(data, childStart, box.end, false, depth+1, s)
		}
		out = append(out, box)
		pos = box.end
	}
	if top {
		w.roots = append(w.roots, out...)
	}
	return out
}

func isMP4Container(typ string) bool {
	switch typ {
	case "moov", "trak", "mdia", "minf", "stbl", "edts", "dinf", "udta", "meta", "mvex", "moof", "traf":
		return true
	}
	return false
}

// child returns the first descendant with the given box type, searching only
// along container boundaries.
func child(box *mp4Box, types ...string) *mp4Box {
	if box == nil || len(types) == 0 {
		return nil
	}
	for _, c := range box.children {
		if c.typ == types[0] {
			if len(types) == 1 {
				return c
			}
			return child(c, types[1:]...)
		}
	}
	return nil
}

func readFTYP(data []byte, box *mp4Box) (string, bool) {
	if box.end-box.payload < 4 {
		return "", false
	}
	return printable(string(data[box.payload : box.payload+4])), true
}

// unmarshal decodes a box payload into dst with go-mp4. It reports false on
// any decoding error, which callers turn into a damaged span.
func unmarshal(data []byte, box *mp4Box, dst mp4.IBox) bool {
	if box == nil {
		return false
	}
	r := bytes.NewReader(data[box.payload:box.end])
	total, err := mp4.Unmarshal(r, uint64(box.end-box.payload), dst, mp4.Context{})
	return err == nil && total > 0
}
