package track

import (
	"encoding/binary"
	"errors"
	"io"
	"sort"

	"mediaregress/internal/media"
)

// boxHeader is one parsed ISO-BMFF box header. Sizes are absolute file
// positions: Body is the first byte after the header and End the exclusive end.
type boxHeader struct {
	Type   string
	Offset int64
	Body   int64
	End    int64
}

// PayloadSize returns the number of payload bytes.
func (b boxHeader) PayloadSize() int64 { return b.End - b.Body }

// boxNode is a box in the parsed tree. Nodes for truncated boxes are returned
// with Err set and no children so callers can attribute the first bad byte.
type boxNode struct {
	Hdr      boxHeader
	Children []*boxNode
	Err      *media.Error
}

var containerTypes = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true, "stbl": true,
	"edts": true, "dinf": true, "udta": true, "mvex": true, "moof": true,
	"traf": true, "mfra": true,
	"stsd": true, "dref": true,
}

// sampleEntryTypes carry the visual properties (crop, color, aspect) the
// parser reads; their children start after a fixed sample-entry header.
var sampleEntryTypes = map[string]bool{
	"avc1": true, "avc3": true, "hev1": true, "hvc1": true, "encv": true,
	"mp4v": true, "vp08": true, "vp09": true, "av01": true,
	"mp4a": true, "enca": true,
}

// sampleEntryHeaderSize returns the fixed header size for a sample entry type.
func sampleEntryHeaderSize(typ string) int64 {
	switch typ {
	case "mp4a", "enca":
		return 28
	default:
		return 78
	}
}

// boxTree reads the box hierarchy without loading payloads. Reading stops at
// the first malformed or truncated box; that node carries the structured
// error positioned at the first byte that could not be interpreted.
type boxTree struct {
	r        io.ReaderAt
	fileSize int64
}

func (t *boxTree) parse(start, limit int64) ([]*boxNode, *media.Error) {
	var out []*boxNode
	pos := start
	for pos < limit {
		if limit-pos < 8 {
			// Fewer than 8 bytes left. If they are the tail of the file this
			// is a truncation, otherwise trailing garbage inside a container.
			if limit == t.fileSize {
				return out, media.NewError(media.ClassTruncatedInput, media.StageParse, "", t.fileSize,
					"truncated box header at end of file")
			}
			return out, media.NewError(media.ClassCorruptInput, media.StageParse, "", pos,
				"trailing bytes shorter than a box header")
		}
		hdr, err := t.header(pos, limit)
		if err != nil {
			return out, err
		}
		n := &boxNode{Hdr: hdr}
		out = append(out, n)

		if hdr.End > t.fileSize {
			if !containerTypes[hdr.Type] {
				n.Err = media.NewError(media.ClassTruncatedInput, media.StageParse, "", t.fileSize,
					"box "+hdr.Type+" extends past end of file")
				return out, n.Err
			}
			// A truncated container: descend into what is available so the
			// deepest damaged box is found and can be attributed to a track.
			children, cerr := t.parse(hdr.Body, t.fileSize)
			n.Children = children
			if cerr != nil {
				n.Err = cerr
				return out, cerr
			}
			n.Err = media.NewError(media.ClassTruncatedInput, media.StageParse, "", t.fileSize,
				"container "+hdr.Type+" declared past end of file")
			return out, n.Err
		}
		if hdr.End > limit {
			n.Err = media.NewError(media.ClassCorruptInput, media.StageParse, "", hdr.End,
				"box "+hdr.Type+" extends past its parent")
			return out, n.Err
		}
		if containerTypes[hdr.Type] || sampleEntryTypes[hdr.Type] {
			body := hdr.Body
			if hdr.Type == "stsd" {
				body = hdr.Body + 8 // FullBox + entry_count
				if body >= hdr.End {
					pos = hdr.End
					continue
				}
			} else if hdr.Type == "dref" {
				body = hdr.Body + 8 // FullBox + entry_count
				if body >= hdr.End {
					pos = hdr.End
					continue
				}
			}
			if sampleEntryTypes[hdr.Type] {
				// Sample entry children follow a fixed header: 78 bytes for
				// visual entries (8 box + 70 fields), 28 for audio.
				body = hdr.Body + sampleEntryHeaderSize(hdr.Type)
				if body >= hdr.End {
					pos = hdr.End
					continue
				}
			}
			if sampleEntryTypes[hdr.Type] && hdr.Body+sampleEntryHeaderSize(hdr.Type) > hdr.End {
				// Header-only sample entry (no child boxes): nothing to read.
				pos = hdr.End
				continue
			}
			children, cerr := t.parse(body, hdr.End)
			n.Children = children
			if cerr != nil {
				n.Err = cerr
				return out, cerr
			}
		}
		pos = hdr.End
	}
	return out, nil
}

// header reads one box header and validates its declared extent.
func (t *boxTree) header(offset, limit int64) (boxHeader, *media.Error) {
	var buf [8]byte
	n, err := t.r.ReadAt(buf[:], offset)
	if n < 8 {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		at := offset + int64(n)
		class := media.ClassTruncatedInput
		if at < t.fileSize {
			class = media.ClassCorruptInput
		}
		return boxHeader{}, media.WrapError(class, media.StageParse, "", at,
			"incomplete box header", err)
	}
	size := int64(binary.BigEndian.Uint32(buf[0:4]))
	typ := string(buf[4:8])
	hdr := boxHeader{Type: typ, Offset: offset, Body: offset + 8}
	if size == 1 {
		var ext [8]byte
		n, err := t.r.ReadAt(ext[:], offset+8)
		if n < 8 {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return boxHeader{}, media.WrapError(media.ClassTruncatedInput, media.StageParse, "",
				offset+8+int64(n), "incomplete largesize box header", err)
		}
		size = int64(binary.BigEndian.Uint64(ext[:]))
		hdr.Body = offset + 16
	} else if size == 0 {
		size = limit - offset
	}
	if size < hdr.Body-offset {
		return boxHeader{}, media.NewError(media.ClassCorruptInput, media.StageParse, "", offset,
			"box "+typ+" declares impossible size "+itoa(size))
	}
	hdr.End = offset + size
	return hdr, nil
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// find returns the first descendant with the given type, depth first.
func find(n *boxNode, typ string) *boxNode {
	for _, c := range n.Children {
		if c.Hdr.Type == typ {
			return c
		}
		if got := find(c, typ); got != nil {
			return got
		}
	}
	return nil
}

// findAll returns all descendants of a type in document order.
func findAll(n *boxNode, typ string) []*boxNode {
	var out []*boxNode
	for _, c := range n.Children {
		if c.Hdr.Type == typ {
			out = append(out, c)
		}
		out = append(out, findAll(c, typ)...)
	}
	return out
}

// firstError returns the first node error in document order.
func firstError(n *boxNode) *media.Error {
	if n.Err != nil {
		return n.Err
	}
	// Walk children in file order so the reported failure is the first one a
	// sequential reader would meet, independent of any map ordering.
	kids := append([]*boxNode(nil), n.Children...)
	sort.Slice(kids, func(i, j int) bool { return kids[i].Hdr.Offset < kids[j].Hdr.Offset })
	for _, c := range kids {
		if e := firstError(c); e != nil {
			return e
		}
	}
	return nil
}

// seeker adapts ReaderAt to io.ReadSeeker for go-mp4 unmarshalling.
type seeker struct {
	r   io.ReaderAt
	pos int64
}

func (s *seeker) Read(p []byte) (int, error) {
	n, err := s.r.ReadAt(p, s.pos)
	s.pos += int64(n)
	if n < len(p) && err == nil {
		err = io.EOF
	}
	return n, err
}

func (s *seeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		s.pos = offset
	case io.SeekCurrent:
		s.pos += offset
	default:
		return 0, errors.New("box: unsupported seek origin")
	}
	return s.pos, nil
}

// boxReadable reports whether a box's payload lies entirely within the file.
// Callers skip unmarshalling a box that runs past EOF: the box tree already
// recorded the first missing byte, and a partial payload would only produce a
// duplicate error at a later position.
func boxReadable(n *boxNode, fileSize int64) bool {
	return n != nil && n.Hdr.End <= fileSize && n.Hdr.Body <= fileSize
}
