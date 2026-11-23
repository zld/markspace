// Package parse reads the documented MP4 subset with strict boundary and
// depth checks. Every error carries the absolute input byte offset so that
// downstream stages can trace rendered anomalies back to source bytes.
package parse

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"mediaforensics/internal/model"
)

// Hard limits mandated by the forensic kernel constraints.
const (
	MaxInputSize  = 256 * 1024 * 1024 // 256 MiB
	MaxBoxDepth   = 16
)

// BoxRecord is one node of the validated box tree with its source offset.
type BoxRecord struct {
	Type       string      `json:"type"`
	Offset     int64       `json:"offset"`
	HeaderSize int64       `json:"header_size"`
	Size       int64       `json:"size"`
	Depth      int         `json:"depth"`
	Children   []BoxRecord `json:"children,omitempty"`
}

// Container is the result of a successful structural walk.
type Container struct {
	Size  int64       `json:"size"`
	Boxes []BoxRecord `json:"boxes"`
}

// containers is the subset of box types whose payload is child boxes.
var containers = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true,
	"stbl": true, "dinf": true, "edts": true, "udta": true,
}

// Walk reads every box header via go-mp4, then applies our own stricter
// validation: input size cap, nesting depth cap, length/boundary checks and
// unknown-required-structure rejection. It never skips a malformed box.
func Walk(r io.ReadSeeker, inputSize int64) (*Container, []*model.ForensicError, error) {
	if inputSize <= 0 {
		return nil, nil, model.NewError(model.StageParseBox, "input", -1, -1,
			"empty input")
	}
	if inputSize > MaxInputSize {
		return nil, nil, model.NewError(model.StageParseBox, "size_cap", -1, 0,
			fmt.Sprintf("input %d exceeds 256 MiB limit", inputSize))
	}
	c := &Container{Size: inputSize}
	errs, topAborted, err := walkChildren(r, 0, inputSize, 0, &c.Boxes)
	_ = topAborted // top-level boxes clear abort semantics per box
	if err != nil {
		return c, errs, err
	}
	if err := requireStructures(c); err != nil {
		return c, append(errs, model.AsForensicError(err)), err
	}
	return c, errs, nil
}

// walkChildren walks one container payload. A damaged leaf records a
// non-fatal forensic error and invalidates the remainder of every ancestor
// container subtree (their sibling positions are derived from the corrupted
// length). The flag bubbles up only through containers; the top level clears
// it after each box so independent top-level boxes (e.g. mdat after moov)
// remain fully parseable.
func walkChildren(r io.ReadSeeker, from, limit int64, depth int, sink *[]BoxRecord) ([]*model.ForensicError, bool, error) {
	var errs []*model.ForensicError
	if _, err := r.Seek(from, io.SeekStart); err != nil {
		return errs, false, model.NewError(model.StageParseBox, "seek", -1, from, err.Error())
	}
	pos := from
	aborted := false
	for pos < limit {
		if pos+8 > limit {
			return errs, aborted, model.NewError(model.StageParseBox, "truncated_header", -1, pos,
				"fewer than 8 bytes remain for box header")
		}
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			return errs, aborted, model.NewError(model.StageParseBox, "seek", -1, pos, err.Error())
		}
		bi, err := mp4.ReadBoxInfo(r)
		if err != nil {
			return errs, aborted, model.NewError(model.StageParseBox, "read_header", -1, pos,
				"truncated or unreadable box header: "+err.Error())
		}
		size := int64(bi.Size)
		if size < 8 {
			return errs, aborted, model.NewError(model.StageParseBox, "illegal_length", -1, pos,
				fmt.Sprintf("box size %d smaller than header", size))
		}
		overshoot := pos+size > limit
		if overshoot {
			if depth == 0 || containers[bi.Type.String()] {
				return errs, aborted, model.NewError(model.StageParseBox, "boundary", -1, pos,
					fmt.Sprintf("box %s size %d crosses parent/EOF boundary", bi.Type, size))
			}
			errs = append(errs, model.NewError(model.StageParseBox, "leaf_overshoot", -1, pos,
				fmt.Sprintf("leaf box %s size %d crosses parent boundary", bi.Type, size)))
			aborted = true
		}
		if depth+1 > MaxBoxDepth {
			return errs, aborted, model.NewError(model.StageParseBox, "depth", -1, pos,
				fmt.Sprintf("box %s exceeds max depth %d", bi.Type, MaxBoxDepth))
		}
		rec := BoxRecord{
			Type:       bi.Type.String(),
			Offset:     int64(bi.Offset),
			HeaderSize: int64(bi.HeaderSize),
			Size:       size,
			Depth:      depth + 1,
		}
		if containers[rec.Type] {
			childErrs, childAborted, cerr := walkChildren(r, pos+int64(bi.HeaderSize), pos+size, depth+1, &rec.Children)
			errs = append(errs, childErrs...)
			if cerr != nil {
				*sink = append(*sink, rec)
				return errs, aborted, cerr
			}
			aborted = aborted || childAborted
		}
		*sink = append(*sink, rec)
		if aborted {
			if depth == 0 {
				// Damage was inside this top-level container; trust only its
				// declared boundary and continue with the next independent box.
				aborted = false
				pos += size
			} else {
				// Inside a container the sibling layout is derived from the
				// corrupted length, so the rest of this subtree is unreadable.
				pos = limit
			}
		} else {
			pos += size
		}
	}
	return errs, aborted, nil
}

// requireStructures enforces the mandatory top-level shape of the subset.
func requireStructures(c *Container) error {
	seen := map[string]bool{}
	for _, b := range c.Boxes {
		seen[b.Type] = true
	}
	for _, need := range []string{"ftyp", "moov", "mdat"} {
		if !seen[need] {
			off := int64(-1)
			return model.NewError(model.StageParseBox, "required_structure", -1, off,
				"missing required top-level box: "+need)
		}
	}
	return nil
}

// Find returns the first box of the given type in a depth-first walk of the
// validated tree, plus whether it exists.
func Find(c *Container, typ string) (BoxRecord, bool) {
	for _, b := range c.Boxes {
		if rec, ok := findIn(b, typ); ok {
			return rec, true
		}
	}
	return BoxRecord{}, false
}

func findIn(b BoxRecord, typ string) (BoxRecord, bool) {
	if b.Type == typ {
		return b, true
	}
	for _, ch := range b.Children {
		if rec, ok := findIn(ch, typ); ok {
			return rec, true
		}
	}
	return BoxRecord{}, false
}

// readU32 is a bounds-checked big-endian read used by the table layer.
func readU32(data []byte, off int64) (uint32, error) {
	if off < 0 || off+4 > int64(len(data)) {
		return 0, model.NewError(model.StageParseBox, "read_u32", -1, off,
			"read crosses input boundary")
	}
	return binary.BigEndian.Uint32(data[off:]), nil
}
