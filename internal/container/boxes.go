// Package container validates MP4/fMP4 containers with go-mp4: box
// length and nesting limits, track time bases and sample ranges. Every
// failure is reported at the first locatable raw byte offset, and a
// rejected object never produces an index entry.
package container

import (
	"bytes"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"mediascan/internal/errs"
)

// Safety limits keep untrusted input from exhausting memory or the
// stack. Inputs beyond these limits are rejected, never partially read.
const (
	MaxFileSize int64 = 512 << 20 // 512 MiB
	MaxBoxDepth       = 16
	MaxBoxCount       = 200_000
)

// BoxRecord describes one parsed top-level or nested box.
type BoxRecord struct {
	Type   string
	Offset int64
	Size   int64
	Depth  int
}

// Container is the validated structural view of an MP4 byte stream.
type Container struct {
	Source string
	Size   int64
	Boxes  []BoxRecord
	Tracks []*Track
}

// boxCollector accumulates box records during the go-mp4 callback walk.
type boxCollector struct {
	records []BoxRecord
	depth   int
}

func (bc *boxCollector) handler(h *mp4.ReadHandle) (interface{}, error) {
	bc.depth++
	defer func() { bc.depth-- }()
	if bc.depth > MaxBoxDepth {
		return nil, fmt.Errorf("box nesting exceeds depth limit %d", MaxBoxDepth)
	}
	if len(bc.records) >= MaxBoxCount {
		return nil, fmt.Errorf("box count exceeds limit %d", MaxBoxCount)
	}
	bi := h.BoxInfo
	// A box whose declared end runs past the input is a truncation.
	end := int64(bi.Offset + bi.Size)
	if bi.Size > uint64(MaxFileSize) || end < int64(bi.Offset) {
		return nil, fmt.Errorf("box %q declares implausible size %d", bi.Type, bi.Size)
	}
	bc.records = append(bc.records, BoxRecord{
		Type:   bi.Type.String(),
		Offset: int64(bi.Offset),
		Size:   int64(bi.Size),
		Depth:  bc.depth,
	})
	// Only structural containers have child boxes. go-mp4's Expand()
	// positions siblings using the payload size actually consumed by
	// UnmarshalAny, which for leaf full boxes (stss, stco, ...) is their
	// struct width rather than the declared payload: descending into a
	// leaf therefore corrupts the sibling offset. Never do that.
	if isContainerBox(bi.Type.String()) {
		return h.Expand()
	}
	return nil, nil
}

// isContainerBox reports whether a box type structurally contains boxes.
func isContainerBox(typ string) bool {
	switch typ {
	case "moov", "trak", "mdia", "minf", "stbl", "dinf", "edts",
		"udta", "mvex", "moof", "traf", "mfra", "sinf", "schi", "iprp",
		"ipco", "meco", "mere":
		return true
	}
	return false
}

// Parse walks the MP4 structure, enforcing size/depth/count limits and
// returning the first locatable offset on any structural failure.
func Parse(source string, raw []byte) (*Container, error) {
	if int64(len(raw)) > MaxFileSize {
		return nil, errs.New(errs.KindContainer, source, 0, errs.Unknown, 0,
			"file size %d exceeds limit %d", len(raw), MaxFileSize)
	}
	c := &Container{Source: source, Size: int64(len(raw))}
	// Independent header walk first: go-mp4 silently stops at a truncated
	// box header, so the byte-level scanner is the authoritative detector
	// for cut-off boxes and sizes that run past EOF.
	if off := scanTruncationOffset(raw); off >= 0 {
		return nil, errs.New(errs.KindContainer, source, off, errs.Unknown, 0,
			"truncated or oversized box header at offset %d", off)
	}
	bc := &boxCollector{}
	r := bytes.NewReader(raw)
	if _, err := mp4.ReadBoxStructure(r, bc.handler); err != nil {
		return nil, locateStructureError(source, raw, err)
	}
	if len(bc.records) == 0 {
		return nil, errs.New(errs.KindContainer, source, 0, errs.Unknown, 0,
			"no MP4 boxes found")
	}
	c.Boxes = bc.records
	if err := c.validateTopLevel(); err != nil {
		return nil, err
	}
	var err error
	c.Tracks, err = extractTracks(source, raw, c)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// locateStructureError maps a go-mp4 parse failure onto the offset of the
// box being read when the failure occurred. When the library gives no
// seek position, the last complete box end is the first suspect offset.
func locateStructureError(source string, raw []byte, parseErr error) error {
	off := scanTruncationOffset(raw)
	return errs.New(errs.KindContainer, source, off, errs.Unknown, 0,
		"malformed MP4 structure: %v", parseErr)
}

// containerBoxes are structural boxes whose payload is more boxes rather
// than media data, so truncation scanning must recurse into them.
var containerBoxes = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true,
	"stbl": true, "dinf": true, "edts": true, "udta": true,
}

// scanTruncationOffset walks box headers manually to find the first box
// whose declared length cannot be satisfied by the remaining bytes. It
// recurses into structural containers so the offset points at the deepest
// first bad box, not merely the enclosing top-level box.
func scanTruncationOffset(raw []byte) int64 {
	var scan func(region []byte, base int64) int64
	scan = func(region []byte, base int64) int64 {
		var pos int64
		for pos < int64(len(region)) {
			// A non-empty tail shorter than an 8-byte box header is a
			// truncated header: the box declaring these bytes is cut off.
			if int64(len(region))-pos < 8 {
				return base + pos
			}
			headerEnd, boxEnd, ok := readBoxBounds(region[pos:])
			if !ok || boxEnd <= 0 || boxEnd > int64(len(region))-pos {
				return base + pos // truncated header or size past EOF
			}
			if len(region[pos:]) >= 8 {
				typ := string(region[pos+4 : pos+8])
				if containerBoxes[typ] {
					child := region[pos+headerEnd : pos+boxEnd]
					if bad := scan(child, base+pos+headerEnd); bad >= 0 {
						return bad
					}
				}
			}
			pos += boxEnd
		}
		return -1 // every header in this region is internally consistent
	}
	return scan(raw, 0)
}

// readBoxBounds returns header length and total box length relative to
// the start of the slice, honoring 64-bit largesize and size==0 EOF.
func readBoxBounds(b []byte) (headerLen, total int64, ok bool) {
	if len(b) < 8 {
		return 0, 0, false
	}
	size32 := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	switch size32 {
	case 1: // 64-bit largesize
		if len(b) < 16 {
			return 0, 0, false
		}
		large := uint64(0)
		for i := 0; i < 8; i++ {
			large = large<<8 | uint64(b[8+i])
		}
		if large < 16 || large > uint64(MaxFileSize) {
			return 16, 0, false
		}
		return 16, int64(large), true
	case 0: // box extends to EOF
		return 8, int64(len(b)), true
	default:
		if size32 < 8 {
			return 8, 0, false
		}
		return 8, int64(size32), true
	}
}

// validateTopLevel requires ftyp up front and rejects boxes whose
// declared offsets overlap inside the file.
func (c *Container) validateTopLevel() error {
	if len(c.Boxes) == 0 || c.Boxes[0].Type != "ftyp" {
		return errs.New(errs.KindContainer, c.Source, 0, errs.Unknown, 0,
			"first top-level box must be ftyp, got %q", firstType(c.Boxes))
	}
	var cursor int64
	for _, b := range c.Boxes {
		if b.Depth != 1 {
			continue
		}
		if b.Offset < cursor {
			return errs.New(errs.KindContainer, c.Source, b.Offset, errs.Unknown, 0,
				"top-level box %q overlaps previous box", b.Type)
		}
		if b.Offset+b.Size > c.Size {
			return errs.New(errs.KindContainer, c.Source, b.Offset, errs.Unknown, 0,
				"box %q ends at %d past file size %d (truncated)",
				b.Type, b.Offset+b.Size, c.Size)
		}
		cursor = b.Offset + b.Size
	}
	return nil
}

func firstType(boxes []BoxRecord) string {
	if len(boxes) == 0 {
		return "<none>"
	}
	return boxes[0].Type
}

// ReadSeekerAt is the subset of file operations the container needs.
type ReadSeekerAt interface {
	io.ReadSeeker
	io.ReaderAt
}
