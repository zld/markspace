// Package inspect reads fragmented MP4 structure directly and reports damage
// with byte offsets, without trusting the sample tables to be well-formed.
package inspect

import (
	"encoding/binary"
	"fmt"
	"io"

	"archivecheck/internal/model"
)

// Box is one top-level box header located in a fragment.
type Box struct {
	Type   string `json:"type"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	// HdrLen is 8 or 16 for extended-size boxes.
	HdrLen int64 `json:"hdr_len"`
}

// PayloadOffset returns the absolute offset of the box payload.
func (b Box) PayloadOffset() int64 { return b.Offset + b.HdrLen }

// Sample is one trun sample with its absolute file position.
type Sample struct {
	// DecodeTime is in the track timescale.
	DecodeTime uint64 `json:"decode_time"`
	Duration   uint32 `json:"duration"`
	Size       uint32 `json:"size"`
	// Flags are the raw sample flags.
	Flags uint32 `json:"flags"`
	// CompositionOffset is the signed composition time offset.
	CompositionOffset int32 `json:"composition_offset"`
	// Sync marks a sync sample (keyframe).
	Sync bool `json:"sync"`
	// DataOffset is the absolute file offset of the sample payload.
	DataOffset int64 `json:"data_offset"`
	// TrunOffset is the absolute file offset of the trun box that declared
	// the sample, used to localise sample-table truncation.
	TrunOffset int64 `json:"trun_offset"`
	// Sequence is the 0-based sample index inside the fragment.
	Sequence int `json:"sequence"`
}

// PresentationTime returns DecodeTime displaced by the composition offset.
func (s Sample) PresentationTime() uint64 {
	return uint64(int64(s.DecodeTime) + int64(s.CompositionOffset))
}

// Track is the decoded track metadata for one fragment.
type Track struct {
	TrackID   uint32 `json:"track_id"`
	Timescale uint32 `json:"timescale"`
	Handler   string `json:"handler"`
	// Samples are in decode order.
	Samples []Sample `json:"samples"`
	// SyncSamples lists the sample indices that are sync samples, in order.
	SyncSamples []int `json:"sync_samples"`
}

// Fragment is the parsed structure of one media segment.
//
// The raw bytes are released as soon as the sample tables are decoded: audio
// and video payloads never need to be retained to audit structure, cadence or
// damage, so memory stays proportional to the number of samples, not bytes.
type Fragment struct {
	// File is the segment file name.
	File string `json:"file"`
	// data is the raw fragment bytes, released by LoadSamples.
	data []byte
	// Size is the total number of bytes read.
	Size int64 `json:"size"`
	// RangeStart is the resource offset the fragment was extracted from, or 0
	// for a standalone file. Box offsets stay fragment-local; this value is
	// added when reporting absolute resource positions.
	RangeStart int64 `json:"range_start,omitempty"`
	// Boxes are the top-level boxes in file order.
	Boxes []Box `json:"boxes"`
	// Tracks are the tracks found, ordered by track ID.
	Tracks []Track `json:"tracks"`
	// Truncated records the first structural problem found, if any.
	Truncated *model.Fault `json:"-"`
	// Faults collects every structural fault found.
	Faults []*model.Fault `json:"-"`
}

// Options bounds how much of a fragment is read.
type Options struct {
	// MaxBoxes caps top-level boxes per fragment.
	MaxBoxes int
	// MaxSamples caps samples per track.
	MaxSamples int
}

const (
	defaultMaxBoxes   = 4096
	defaultMaxSamples = 500000
)

func (o Options) maxBoxes() int {
	if o.MaxBoxes <= 0 {
		return defaultMaxBoxes
	}
	return o.MaxBoxes
}

func (o Options) maxSamples() int {
	if o.MaxSamples <= 0 {
		return defaultMaxSamples
	}
	return o.MaxSamples
}

// SizeReader reads the whole fragment up to limit bytes. It enforces the
// bounded-memory contract at the segment level: a fragment larger than limit
// is refused rather than silently truncated.
type SizeReader struct {
	R     io.Reader
	Limit int64
	// Exact declares that the reader is expected to yield exactly Limit bytes,
	// as with a byte range, so reaching the limit is success rather than an
	// overflow.
	Exact bool
}

// ReadAll reads at most Limit bytes and reports an error if more remain.
func (s SizeReader) ReadAll() ([]byte, error) {
	probe := s.Limit + 1
	if s.Exact {
		probe = s.Limit
	}
	lr := io.LimitReader(s.R, probe)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > s.Limit {
		return nil, fmt.Errorf("segment exceeds %d byte limit", s.Limit)
	}
	return data, nil
}

// ParseFragment decodes the top-level structure of an fMP4 fragment. Sample
// tables are loaded separately by LoadSamples so structural faults can be
// localised before any payload interpretation.
func ParseFragment(name string, data []byte, init *Init, opts Options) (*Fragment, error) {
	f := &Fragment{File: name, Size: int64(len(data)), data: data}
	if err := walkBoxes(f, data, opts); err != nil {
		return f, err
	}
	return f, nil
}

// release drops the raw fragment bytes once sample metadata is extracted.
func (f *Fragment) release() { f.data = nil }

// BytesHeld reports whether the raw fragment bytes are still in memory.
func (f *Fragment) BytesHeld() bool { return f.data != nil }

func (f *Fragment) add(fault *model.Fault) {
	if fault == nil {
		return
	}
	// Faults found during sample loading are still fragment-local; shift them
	// onto the resource the fragment was extracted from.
	if f.RangeStart > 0 {
		rebaseFault(fault, f.RangeStart)
	}
	f.Faults = append(f.Faults, fault)
	if f.Truncated == nil {
		f.Truncated = fault
	}
}

func src(name string, offset int64) model.Source {
	return model.Source{Layer: "inspect", File: name, Offset: offset}
}

// walkBoxes iterates top-level boxes, validating every declared length against
// the bytes actually present.
func walkBoxes(f *Fragment, data []byte, opts Options) error {
	var pos int64
	count := 0
	for pos < int64(len(data)) {
		count++
		if count > opts.maxBoxes() {
			fault := model.NewFault(model.CodeMP4Structure, model.SeverityError,
				src(f.File, pos), "more than %d top-level boxes", opts.maxBoxes())
			f.add(fault)
			return fault
		}
		box, err := readBoxHeader(f.File, data, pos)
		if err != nil {
			f.add(err)
			return err
		}
		if box.Size < box.HdrLen {
			fault := model.NewFault(model.CodeMP4BadLength, model.SeverityError,
				src(f.File, pos), "%q declares size %d below its %d-byte header", box.Type, box.Size, box.HdrLen).
				WithBytes(box.Offset, box.Offset+box.HdrLen)
			f.add(fault)
			return fault
		}
		end := pos + box.Size
		if end > int64(len(data)) {
			fault := model.NewFault(model.CodeMP4BoxTruncated, model.SeverityError,
				src(f.File, pos), "%q declares %d bytes but only %d remain", box.Type, box.Size, int64(len(data))-pos).
				WithBytes(pos, int64(len(data)))
			f.add(fault)
			return fault
		}
		f.Boxes = append(f.Boxes, box)
		pos = end
	}
	return nil
}

func readBoxHeader(name string, data []byte, pos int64) (Box, *model.Fault) {
	if int64(len(data))-pos < 8 {
		return Box{}, model.NewFault(model.CodeMP4BoxTruncated, model.SeverityError,
			src(name, pos), "only %d bytes left for a box header", int64(len(data))-pos).
			WithBytes(pos, int64(len(data)))
	}
	size := int64(binary.BigEndian.Uint32(data[pos:]))
	typ := string(data[pos+4 : pos+8])
	hdr := int64(8)
	if size == 1 {
		if int64(len(data))-pos < 16 {
			return Box{}, model.NewFault(model.CodeMP4BoxTruncated, model.SeverityError,
				src(name, pos), "%q has a truncated 64-bit size", typ).
				WithBytes(pos, int64(len(data)))
		}
		size = int64(binary.BigEndian.Uint64(data[pos+8:]))
		hdr = 16
	} else if size == 0 {
		return Box{}, model.NewFault(model.CodeMP4BadLength, model.SeverityError,
			src(name, pos), "%q declares size 0 (extends to end of file), which is not supported", typ).
			WithBytes(pos, int64(len(data)))
	}
	return Box{Type: typ, Offset: pos, Size: size, HdrLen: hdr}, nil
}

// FindBox returns the first top-level box of the given type.
func (f *Fragment) FindBox(typ string) (Box, bool) {
	for _, b := range f.Boxes {
		if b.Type == typ {
			return b, true
		}
	}
	return Box{}, false
}

// BoxCount returns how many top-level boxes of the type appear.
func (f *Fragment) BoxCount(typ string) int {
	n := 0
	for _, b := range f.Boxes {
		if b.Type == typ {
			n++
		}
	}
	return n
}

// ParseRange decodes a fragment that was extracted from a byte range of a
// larger resource. start is the range's offset in the resource; every reported
// byte offset is rebased onto the resource so a report points at the same byte
// an archivist would open. The fragment is otherwise treated exactly like a
// standalone file.
func ParseRange(name string, data []byte, start, length int64, init *Init, opts Options) (*Fragment, error) {
	if start < 0 || length < 0 {
		return nil, model.NewFault(model.CodeMP4BadLength, model.SeverityError,
			model.Source{Layer: "inspect", File: name, Offset: start, Bytes: length},
			"range [%d,%d) is invalid", start, start+length)
	}
	if int64(len(data)) < length {
		return nil, model.NewFault(model.CodeMP4BoxTruncated, model.SeverityError,
			model.Source{Layer: "inspect", File: name, Offset: start + int64(len(data)), Bytes: length},
			"range declares %d bytes but only %d were read", length, len(data)).
			WithBytes(start, start+int64(len(data)))
	}
	f, err := ParseFragment(name, data, init, opts)
	if f != nil {
		// Box offsets stay fragment-local so the payload can still be sliced;
		// reported faults are rebased onto the resource. LoadSamples adds more
		// faults later, so it rebases them using RangeStart.
		f.RangeStart = start
		rebaseFault(f.Truncated, start)
		for _, flt := range f.Faults {
			rebaseFault(flt, start)
		}
	}
	return f, err
}

// rebaseFault shifts a fault's byte offsets from fragment-local to
// resource-relative coordinates.
func rebaseFault(f *model.Fault, base int64) {
	if f == nil || base == 0 {
		return
	}
	if f.Location.ByteSet {
		f.Location.ByteStart += base
		f.Location.ByteEnd += base
	}
	// Source.Offset is -1 for "not applicable"; -1+base would be a bogus
	// absolute position, so only real offsets are rebased.
	if f.Source.Offset >= 0 {
		f.Source.Offset += base
	}
}
