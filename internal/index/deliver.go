// Package index (delivery part) writes the repaired fMP4 and its rebuilt
// index. Media sample payloads are copied verbatim: only fragment structure,
// timestamps and index are rewritten.
package index

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"io"

	"mediaroute/internal/model"
)

// Delivery is the outcome of writing a repaired file.
type Delivery struct {
	Index       *model.Index
	Bytes       uint64
	SampleBytes uint64
	Boxes       []model.ByteRange
	Sha256      string
}

// WriteOptions control path-safe delivery.
type WriteOptions struct {
	// BaseOffset is the absolute offset of the first output byte (0 for a
	// file written from scratch).
	BaseOffset uint64
}

// tfdtPatch describes an in-place timestamp rewrite.
type tfdtPatch struct {
	at    uint64 // offset within the copied moof bytes
	value uint64
	width int
}

// collectPatches derives patch positions from the fragment's tracks. The
// patch offset recorded during ingest is absolute in the source; Convert
// them to moof-relative positions here.
func collectPatches(frag *model.Fragment) []tfdtPatch {
	patches := make([]tfdtPatch, 0, len(frag.Tracks))
	for ti := range frag.Tracks {
		ft := &frag.Tracks[ti]
		if ft.TfdtPatchAt == 0 && !ft.Repaired {
			continue // nothing to rewrite
		}
		rel := int64(ft.TfdtPatchAt) - int64(frag.MoofRange.Start)
		if rel < 0 || uint64(rel) >= frag.MoofRange.Len() {
			continue
		}
		width := 4
		if ft.TfdtVersion == 1 {
			width = 8
		}
		if uint64(rel)+uint64(width) > uint64(len(frag.Moof)) {
			continue
		}
		patches = append(patches, tfdtPatch{at: uint64(rel), value: ft.Tfdt, width: width})
	}
	return patches
}

// applyPatches rewrites the base media decode time in a copy of the moof.
func applyPatches(moof []byte, patches []tfdtPatch) ([]byte, error) {
	if len(patches) == 0 {
		return moof, nil
	}
	out := append([]byte(nil), moof...)
	for _, p := range patches {
		if p.width == 4 {
			if p.value > 0xFFFFFFFF {
				return nil, model.NewTaskError(model.CategoryInvalidBox, Stage,
					fmt.Sprintf("tfdt value %d does not fit version 0 field", p.value))
			}
			binary.BigEndian.PutUint32(out[p.at:p.at+4], uint32(p.value))
		} else {
			binary.BigEndian.PutUint64(out[p.at:p.at+8], p.value)
		}
	}
	return out, nil
}

// WriteRepaired streams the init segment followed by every delivered
// fragment to w, rewriting repaired tfdt values and building the index from
// the exact offsets written. It is safe to call with a nil index target.
func WriteRepaired(w io.Writer, init []byte, frags []model.Fragment, opt WriteOptions) (*Delivery, error) {
	return writeRepaired(w, init, nil, frags, opt)
}

// WriteRepairedSet renders a whole source set: the init segment, then any
// preserved non-fragment boxes, then the delivered fragments. Preserved boxes
// keep their original bytes and so keep unknown box types intact.
func WriteRepairedSet(w io.Writer, set *model.SourceSet, opt WriteOptions) (*Delivery, error) {
	if set == nil {
		return nil, model.NewTaskError(model.CategoryInvalidConfig, Stage, "nil source set")
	}
	return writeRepaired(w, initBytes(set), preservedBytes(set), set.Fragments, opt)
}

func initBytes(set *model.SourceSet) []byte {
	if set.Init == nil {
		return nil
	}
	return set.Init.Bytes
}

// preservedBytes concatenates the preserved boxes in source order.
func preservedBytes(set *model.SourceSet) [][]byte {
	if len(set.Preserved) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(set.Preserved))
	for _, p := range set.Preserved {
		out = append(out, p.Bytes)
	}
	return out
}

func writeRepaired(w io.Writer, init []byte, preserved [][]byte, frags []model.Fragment, opt WriteOptions) (*Delivery, error) {
	d := &Delivery{Index: &model.Index{Entries: make([]model.IndexEntry, 0, len(frags))}}
	h := sha256.New()
	dst := io.MultiWriter(w, h)
	cursor := opt.BaseOffset
	writeBox := func(b []byte, what string) error {
		n, err := dst.Write(b)
		if err != nil {
			return model.WrapTaskError(model.CategoryIO, Stage, "write "+what, err).WithOffset(cursor)
		}
		d.Bytes += uint64(n)
		d.Boxes = append(d.Boxes, model.RangeOf(cursor, uint64(n)))
		d.Index.Boundaries = append(d.Index.Boundaries, model.RangeOf(cursor, uint64(n)))
		cursor += uint64(n)
		return nil
	}
	if len(init) > 0 {
		if err := writeBox(init, "init segment"); err != nil {
			return nil, err
		}
	}
	for _, p := range preserved {
		if err := writeBox(p, "preserved box"); err != nil {
			return nil, err
		}
	}
	prevEnd := make(map[uint32]model.Micros, 4)
	for i := range frags {
		frag := &frags[i]
		if frag.Discarded || frag.Truncated {
			continue
		}
		patches := collectPatches(frag)
		moof, err := applyPatches(frag.Moof, patches)
		if err != nil {
			if te, ok := model.AsTaskError(err); ok {
				return nil, te.WithOffset(frag.MoofRange.Start)
			}
			return nil, err
		}
		moofOff := cursor
		n, err := dst.Write(moof)
		if err != nil {
			return nil, model.WrapTaskError(model.CategoryIO, Stage, "write moof", err).WithOffset(moofOff)
		}
		if uint64(n) != frag.MoofRange.Len() {
			return nil, model.NewTaskError(model.CategoryInvalidBox, Stage,
				"rewritten moof changed length; lengths must be preserved").WithOffset(moofOff)
		}
		d.Bytes += uint64(n)
		d.Boxes = append(d.Boxes, model.RangeOf(moofOff, uint64(n)))
		d.Index.Boundaries = append(d.Index.Boundaries, model.RangeOf(moofOff, uint64(n)))
		cursor += uint64(n)

		mdatOff := cursor
		n, err = dst.Write(frag.Mdat)
		if err != nil {
			return nil, model.WrapTaskError(model.CategoryIO, Stage, "write mdat", err).WithOffset(mdatOff)
		}
		if uint64(n) != frag.MdatRange.Len() {
			return nil, model.NewTaskError(model.CategoryInvalidBox, Stage,
				"short mdat write").WithOffset(mdatOff)
		}
		d.Bytes += uint64(n)
		d.SampleBytes += uint64(n)
		d.Boxes = append(d.Boxes, model.RangeOf(mdatOff, uint64(n)))
		d.Index.Boundaries = append(d.Index.Boundaries, model.RangeOf(mdatOff, uint64(n)))
		cursor += uint64(n)

		for ti := range frag.Tracks {
			entry, err := entryFor(&frag.Tracks[ti], frag.Index, moofOff, mdatOff, prevEnd)
			if err != nil {
				if err == errNoEntry {
					continue
				}
				return nil, err
			}
			d.Index.Entries = append(d.Index.Entries, entry)
		}
	}
	d.Index.TotalBytes = cursor
	d.Sha256 = finishSHA(h)
	return d, nil
}

func finishSHA(h hash.Hash) string {
	return hex.EncodeToString(h.Sum(nil))
}

// SortedEntries returns index entries in track/time order for deterministic
// serialisation.
func (d *Delivery) SortedEntries() []model.IndexEntry { return d.Index.Sorted() }
