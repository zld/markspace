package ingest

import (
	"fmt"

	"github.com/abema/go-mp4"

	"mediacore/internal/fault"
)

// IndexReference is one sidx subsegment reference.
type IndexReference struct {
	Index         int    `json:"index"`
	Size          int64  `json:"size"`
	Duration      int64  `json:"duration"`
	StartsWithSAP bool   `json:"starts_with_sap"`
	SAPType       uint32 `json:"sap_type"`
}

// Index is a parsed segmentation index, possibly incomplete.
type Index struct {
	File        string           `json:"file"`
	Offset      int64            `json:"offset"`
	Size        int64            `json:"size"`
	ReferenceID uint32           `json:"reference_id"`
	Timescale   uint32           `json:"timescale"`
	References  []IndexReference `json:"references"`
	// DeclaredReferences is what the box claims, which may exceed the
	// references that could actually be decoded from the available bytes.
	DeclaredReferences int  `json:"declared_references"`
	Complete           bool `json:"complete"`
}

// ScanIndex reads the first sidx after the init segment. A missing or
// truncated index is not fatal: it is reported and the caller falls back to a
// bounded moof scan. found=false with a nil fault means there is no sidx.
func ScanIndex(r *Reader, start int64) (*Index, *fault.Error) {
	for off := start; off < r.Len(); {
		h, ok, f := r.Next(off)
		if !ok {
			if f != nil && f.Code == fault.CodeTruncatedHeader {
				return nil, f
			}
			return nil, nil
		}
		switch h.Type {
		case "moof":
			// The fragment region started without an index.
			return nil, nil
		case "sidx":
			return r.parseSidx(h)
		}
		off = h.End()
	}
	return nil, nil
}

func (r *Reader) parseSidx(h Header) (*Index, *fault.Error) {
	var sidx mp4.Sidx
	f := r.Unmarshal(h, &sidx)
	if f != nil {
		// A partially written index is a recoverable condition: report it and
		// let the bounded scan recover from moof headers.
		idx := &Index{File: r.File, Offset: h.Offset, Size: h.Size, Complete: false}
		return idx, &fault.Error{Code: fault.CodeIndexGap, File: r.File, Offset: h.Offset, BoxType: "sidx",
			Detail: "index truncated or corrupt: " + f.Detail}
	}
	idx := &Index{
		File:               r.File,
		Offset:             h.Offset,
		Size:               h.Size,
		ReferenceID:        sidx.ReferenceID,
		Timescale:          sidx.Timescale,
		DeclaredReferences: int(sidx.ReferenceCount),
		Complete:           true,
	}
	if sidx.Timescale == 0 {
		return idx, fault.New(fault.CodeIndexGap, r.File, h.Offset, "sidx", "index timescale must be positive")
	}
	if int(sidx.ReferenceCount) != len(sidx.References) {
		idx.Complete = false
		return idx, fault.New(fault.CodeIndexGap, r.File, h.Offset, "sidx",
			fmt.Sprintf("declared %d references, decoded %d", sidx.ReferenceCount, len(sidx.References)))
	}
	for i, ref := range sidx.References {
		if ref.ReferencedSize == 0 {
			idx.Complete = false
			return idx, fault.New(fault.CodeIndexGap, r.File, h.Offset, "sidx",
				fmt.Sprintf("reference %d has zero size", i))
		}
		idx.References = append(idx.References, IndexReference{
			Index:         i,
			Size:          int64(ref.ReferencedSize),
			Duration:      int64(ref.SubsegmentDuration),
			StartsWithSAP: ref.StartsWithSAP,
			SAPType:       ref.SAPType,
		})
	}
	return idx, nil
}

func fragmentDuration(f Fragment) int64 {
	var total int64
	for _, s := range f.Samples {
		if s.Duration > 0 {
			total += s.Duration
		}
	}
	return total
}

func hasCode(errs []*fault.Error, code fault.Code) bool {
	for _, e := range errs {
		if e.Code == code {
			return true
		}
	}
	return false
}

// ScanResult is the outcome of the bounded moof scan used when the index is
// missing or short.
type ScanResult struct {
	Fragments    []Fragment     `json:"fragments"`
	Errors       []*fault.Error `json:"-"`
	ScannedBoxes int            `json:"scanned_boxes"`
	Stopped      bool           `json:"stopped"`
	// ResumeAt is the first byte not consumed, suitable for a checkpoint.
	ResumeAt int64 `json:"resume_at"`
	// IndexGap is set when the index does not cover every fragment in the file.
	IndexGap bool `json:"index_gap"`
	// IndexCovered is the number of fragments the sidx described.
	IndexCovered int `json:"index_covered"`
}

// ScanFragmentsBounded walks top-level boxes from start, building fragments
// without an index. It stops at a corrupt mdat boundary, a box-count limit or
// a declared fragment duration over the limit, and returns a resume offset.
func (r *Reader) ScanFragmentsBounded(start int64, idx *Index) *ScanResult {
	res := &ScanResult{ResumeAt: start}
	expected := 0
	if idx != nil && len(idx.References) > 0 {
		expected = len(idx.References)
		res.IndexCovered = expected
	} else if idx != nil {
		res.IndexGap = true
	}
	limit := r.Limits.MaxScanBoxes
	off := start
	covered := 0
	for off < r.Len() {
		if limit > 0 && res.ScannedBoxes >= limit {
			res.Stopped = true
			res.Errors = append(res.Errors, fault.New(fault.CodeScanLimit, r.File, off, "",
				fmt.Sprintf("scan stopped after %d boxes; resume at %d", res.ScannedBoxes, off)))
			res.ResumeAt = off
			return res
		}
		h, ok, f := r.Next(off)
		if !ok {
			if f != nil {
				res.Errors = append(res.Errors, f)
			}
			res.ResumeAt = off
			return res
		}
		res.ScannedBoxes++
		if h.Type != "moof" {
			// Only declared sizes are followed. In particular an mdat is
			// stepped over as one box: its payload is never scanned for box
			// magic, so a corrupt payload cannot masquerade as a fragment.
			off = h.End()
			continue
		}
		frag, more, f := r.parseMoof(h, len(res.Fragments))
		res.Errors = append(res.Errors, more...)
		if f != nil {
			res.Errors = append(res.Errors, f)
			if f.Code == fault.CodeTruncatedBox || f.Code == fault.CodeSampleOutOfRange {
				// The declared range is untrustworthy. Stop rather than
				// resynchronise, and leave the resume point at this moof.
				res.ResumeAt = h.Offset
				res.Stopped = true
				return res
			}
			off = h.End()
			continue
		}
		if r.Limits.MaxFragmentDuration > 0 && fragmentDuration(*frag) > r.Limits.MaxFragmentDuration {
			res.Errors = append(res.Errors, fault.New(fault.CodeDurationLimit, r.File, h.Offset, "moof",
				fmt.Sprintf("fragment at %d spans %d ticks, limit %d", h.Offset, fragmentDuration(*frag), r.Limits.MaxFragmentDuration)).WithFragment(frag.Sequence))
			res.ResumeAt = h.Offset
			res.Stopped = true
			return res
		}
		res.Fragments = append(res.Fragments, *frag)
		if expected > 0 && len(res.Fragments) > expected {
			// More fragments than the index promised: the index is short. The
			// fragment is still valid, so the scan continues, but the first
			// uncovered fragment is reported at its moof header.
			res.IndexGap = true
			if len(res.Errors) == 0 || !hasCode(res.Errors, fault.CodeIndexGap) {
				res.Errors = append(res.Errors, fault.New(fault.CodeIndexGap, r.File, h.Offset, "moof",
					fmt.Sprintf("index covers %d of %d fragments", expected, len(res.Fragments))))
			}
		}
		if idx != nil && covered < len(idx.References) {
			want := idx.References[covered].Size
			if want != frag.MdatOffset+frag.MdatSize-h.Offset {
				res.IndexGap = true
			}
		}
		covered++
		off = frag.End()
		res.ResumeAt = off
	}
	return res
}
