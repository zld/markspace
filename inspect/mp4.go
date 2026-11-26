// Package inspect walks fMP4 box structure and AVC access units to report
// keyframe timing and corrupted segments with exact byte offsets.
package inspect

import (
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"

	"streamcheck/core"
)

const maxBoxDepth = 4

// Box is one top-level box in the file.
type Box struct {
	Type   string `json:"type"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
}

// Segment is one moof+mdat pair with keyframe and corruption findings.
type Segment struct {
	Index         int      `json:"index"`
	MoofOffset    int64    `json:"moofOffset"`
	DecodeTime    core.Rat `json:"decodeTime"`
	MdatOffset    int64    `json:"mdatOffset"`
	MdatSize      int64    `json:"mdatSize"`
	Keyframe      bool     `json:"keyframe"`
	Corrupt       bool     `json:"corrupt"`
	CorruptOffset int64    `json:"corruptOffset"`
}

// Report is the structural inspection of one fMP4 file.
type Report struct {
	Path     string    `json:"path"`
	Boxes    []Box     `json:"boxes"`
	Segments []Segment `json:"segments"`
}

// InspectMP4 walks the top-level box structure of an fMP4 file, pairing
// moof boxes with the following mdat and reading tfdt decode times.
// Truncated or malformed boxes are structured errors, never repaired.
func InspectMP4(path string, timescale int64) (*Report, error) {
	if timescale <= 0 {
		timescale = 1000
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, core.E(core.KindInput, "open", "cannot open fMP4", err).At(path, -1)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, core.E(core.KindInput, "stat", "cannot stat fMP4", err).At(path, -1)
	}
	size := st.Size()
	if size > 64<<20 {
		return nil, core.E(core.KindInput, "open", "input exceeds 64MiB limit", nil).At(path, -1)
	}
	rep := &Report{Path: path}
	off := int64(0)
	var pending *Segment
	for off < size {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return nil, core.E(core.KindInspect, "seek", "seek failed", err).At(path, off)
		}
		bi, err := mp4.ReadBoxInfo(f)
		if err != nil {
			return nil, core.E(core.KindInspect, "read-box", "truncated or invalid box header", err).At(path, off)
		}
		end := off + int64(bi.Size)
		if bi.ExtendToEOF {
			end = size
		}
		if end > size || int64(bi.Size) < int64(bi.HeaderSize) {
			return nil, core.E(core.KindInspect, "read-box",
				"truncated box: declared size exceeds file", nil).At(path, off)
		}
		rep.Boxes = append(rep.Boxes, Box{Type: bi.Type.String(), Offset: off, Size: end - off})
		switch bi.Type {
		case mp4.BoxTypeMoof():
			seg := Segment{Index: len(rep.Segments), MoofOffset: off, CorruptOffset: -1}
			if err := readTfdt(f, bi, timescale, &seg); err != nil {
				return nil, err.At(path, off)
			}
			pending = &seg
		case mp4.BoxTypeMdat():
			if pending != nil {
				pending.MdatOffset = off + int64(bi.HeaderSize)
				pending.MdatSize = end - pending.MdatOffset
				rep.Segments = append(rep.Segments, *pending)
				pending = nil
			}
		}
		off = end
	}
	return rep, nil
}

// readTfdt descends into a moof box and extracts the tfdt decode time.
func readTfdt(f *os.File, moof *mp4.BoxInfo, timescale int64, seg *Segment) *core.Error {
	start := int64(moof.Offset) + int64(moof.HeaderSize)
	end := int64(moof.Offset) + int64(moof.Size)
	err := walkChildren(f, start, end, 0, func(bi *mp4.BoxInfo) error {
		if bi.Type != mp4.BoxTypeTfdt() {
			return nil
		}
		if _, err := bi.SeekToPayload(f); err != nil {
			return err
		}
		var tfdt mp4.Tfdt
		if _, err := mp4.Unmarshal(f, bi.Size-bi.HeaderSize, &tfdt, mp4.Context{}); err != nil {
			return err
		}
		seg.DecodeTime = core.NewRat(int64(tfdt.GetBaseMediaDecodeTime()), timescale)
		return nil
	})
	if err != nil {
		return core.E(core.KindInspect, "read-tfdt", "cannot decode tfdt", err)
	}
	return nil
}

// walkChildren invokes fn for every box nested under [start, end),
// recursing into container boxes.
func walkChildren(f *os.File, start, end int64, depth int, fn func(*mp4.BoxInfo) error) error {
	if depth > maxBoxDepth {
		return nil
	}
	for off := start; off < end; {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return err
		}
		bi, err := mp4.ReadBoxInfo(f)
		if err != nil {
			return err
		}
		if err := fn(bi); err != nil {
			return err
		}
		switch bi.Type.String() {
		case "moof", "traf":
			childStart := off + int64(bi.HeaderSize)
			if err := walkChildren(f, childStart, off+int64(bi.Size), depth+1, fn); err != nil {
				return err
			}
		}
		off += int64(bi.Size)
	}
	return nil
}
