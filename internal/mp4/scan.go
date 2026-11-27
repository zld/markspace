// Package mp4scan walks an MP4 container box-by-box, recording the raw
// offset and size of every box and reporting structural problems
// (truncation, oversize boxes, trailing garbage) with the first
// confirmable input offset.
package mp4scan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"

	"mediarebuild/internal/model"
)

// Box is one scanned box with its raw extent.
type Box struct {
	Type   string `json:"type"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	Depth  int    `json:"depth"`
}

// End returns the offset just past the box.
func (b Box) End() int64 { return b.Offset + b.Size }

// Result is the outcome of a container scan.
type Result struct {
	Boxes []Box       `json:"boxes"`
	Diags model.Diags `json:"diags"`
}

// Find returns all boxes of the given 4CC, in file order.
func (r Result) Find(typ string) []Box {
	var out []Box
	for _, b := range r.Boxes {
		if b.Type == typ {
			out = append(out, b)
		}
	}
	return out
}

// containerTypes are boxes whose payload is a box sequence.
var containerTypes = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true,
	"stbl": true, "dinf": true, "edts": true, "moof": true,
	"traf": true, "mvex": true, "udta": true,
}

// Scan walks the container at path. Truncation and structural violations
// are reported as diagnostics; scanning continues where possible.
func Scan(ctx context.Context, path string) (Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Result{}, err
	}
	res := Result{}
	depth := 0
	var handler func(h *mp4.ReadHandle) (interface{}, error)
	handler = func(h *mp4.ReadHandle) (interface{}, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		bi := h.BoxInfo
		box := Box{Type: bi.Type.String(), Offset: int64(bi.Offset), Size: int64(bi.Size), Depth: depth}
		if box.Size < 8 {
			res.Diags.Add(model.NewDiag(model.StageScan, "bad-box-size", box.Offset, false,
				fmt.Sprintf("box %s has invalid size %d", box.Type, box.Size)))
			return nil, nil
		}
		if box.End() > fi.Size() {
			res.Diags.Add(model.NewDiag(model.StageScan, "truncated-box", box.Offset, true,
				fmt.Sprintf("box %s extends to %d, past end of file %d", box.Type, box.End(), fi.Size())))
			return nil, nil
		}
		res.Boxes = append(res.Boxes, box)
		if containerTypes[box.Type] {
			depth++
			_, err := h.Expand()
			depth--
			if err != nil && !errors.Is(err, io.EOF) {
				res.Diags.Add(model.NewDiag(model.StageScan, "expand-failed", box.Offset, true,
					fmt.Sprintf("box %s: %v", box.Type, err)))
			}
		}
		return nil, nil
	}
	_, err = mp4.ReadBoxStructure(f, handler)
	if err != nil && !errors.Is(err, io.EOF) {
		// A read error mid-stream means truncation at the current offset.
		off := int64(0)
		if len(res.Boxes) > 0 {
			off = res.Boxes[len(res.Boxes)-1].End()
		}
		res.Diags.Add(model.NewDiag(model.StageScan, "truncated-container", off, true, err.Error()))
	}
	// Trailing bytes after the last top-level box.
	var lastEnd int64
	for _, b := range res.Boxes {
		if b.Depth == 0 && b.End() > lastEnd {
			lastEnd = b.End()
		}
	}
	if len(res.Boxes) > 0 && lastEnd < fi.Size() {
		d := model.NewDiag(model.StageScan, "trailing-data", lastEnd, true,
			fmt.Sprintf("%d unread bytes after last box", fi.Size()-lastEnd))
		d.EndOffset = fi.Size()
		res.Diags.Add(d)
	}
	if len(res.Boxes) == 0 {
		res.Diags.Add(model.NewDiag(model.StageScan, "empty-container", 0, false, "no boxes found"))
	}
	return res, nil
}
