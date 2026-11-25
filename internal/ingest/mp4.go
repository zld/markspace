// Package ingest parses replay inputs (MP4 container, RTP timestamp events)
// into a timebase-neutral representation. Every defect is reported with the
// byte offset of the box or packet record that caused it.
package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"

	"replaycheck/internal/model"
)

// Limits bounds the allocations performed while expanding sample tables.
// Inputs that exceed a limit are rejected rather than allowed to exhaust
// memory.
type Limits struct {
	MaxBoxes   int
	MaxDepth   int
	MaxSamples int
	MaxBytes   int64
}

// DefaultLimits is the budget used by the CLI.
func DefaultLimits() Limits {
	return Limits{MaxBoxes: 100_000, MaxDepth: 16, MaxSamples: 1_000_000, MaxBytes: 512 << 20}
}

// Box is one parsed MP4 box with its absolute file offset.
type Box struct {
	Offset     uint64
	Size       uint64
	HeaderSize uint64
	Type       string
	Children   []*Box
}

// PayloadOffset is the first byte after the box header.
func (b *Box) PayloadOffset() uint64 { return b.Offset + b.HeaderSize }

// PayloadSize is the declared payload length.
func (b *Box) PayloadSize() uint64 { return b.Size - b.HeaderSize }

// Find returns the first direct child with the given type.
func (b *Box) Find(typ string) *Box {
	for _, c := range b.Children {
		if c.Type == typ {
			return c
		}
	}
	return nil
}

// All returns every direct child with the given type.
func (b *Box) All(typ string) []*Box {
	var out []*Box
	for _, c := range b.Children {
		if c.Type == typ {
			out = append(out, c)
		}
	}
	return out
}

var containerTypes = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true,
	"stbl": true, "edts": true, "dinf": true, "mvex": true,
	"moof": true, "traf": true, "mfra": true, "udta": true,
}

// ByteRange is a half-open range of absolute byte offsets.
type ByteRange struct {
	Offset uint64 `json:"offset"`
	Size   uint64 `json:"size"`
}

// Sample is one media sample from the sample table.
type Sample struct {
	DTS    int64  `json:"dts"`    // media ticks in the track timebase
	Size   uint32 `json:"size"`   // bytes
	Offset uint64 `json:"offset"` // absolute offset in the source file
}

// Track is a validated container track. Samples are expanded lazily enough to
// map timeline ranges to source byte ranges without re-parsing the file.
type Track struct {
	ID           uint32           `json:"id"`
	Kind         string           `json:"kind"`
	Handler      string           `json:"handler"`
	Codec        string           `json:"codec"`
	Timescale    uint32           `json:"timescale"`
	Duration     uint64           `json:"duration"` // media ticks
	Samples      []Sample         `json:"-"`
	SampleCount  int              `json:"sampleCount"`
	MediaBytes   uint64           `json:"mediaBytes"`
	MdhdOffset   uint64           `json:"mdhdOffset"`
	HdlrOffset   uint64           `json:"hdlrOffset"`
	StsdOffset   uint64           `json:"stsdOffset"`
	SttsOffset   uint64           `json:"sttsOffset"`
	StszOffset   uint64           `json:"stszOffset"`
	StscOffset   uint64           `json:"stscOffset"`
	StcoOffset   uint64           `json:"stcoOffset"`
	TrakOffset   uint64           `json:"trakOffset"`
	TimebaseErr  int64            `json:"timebaseRemainder"`
	SampleRanges []model.Rational `json:"sampleRanges,omitempty"`
}

// Container is the parsed MP4 input.
type Container struct {
	Path       string      `json:"path"`
	Size       uint64      `json:"size"`
	Digest     string      `json:"digest"`
	MajorBrand string      `json:"majorBrand"`
	Timescale  uint32      `json:"timescale"`
	Duration   uint64      `json:"duration"`
	BoxCount   int         `json:"boxCount"`
	Tracks     []*Track    `json:"tracks"`
	Mdat       []ByteRange `json:"mdat"`
	Tree       *Box        `json:"-"`
}

// digestFile computes the SHA-256 of the whole file using a bounded buffer.
func digestFile(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.CopyBuffer(h, r, make([]byte, 64<<10)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// scanBoxes parses one level of boxes in [offset,end) and recurses into known
// container boxes. A declared size that runs past the parent bound is a
// truncation and is reported at the box's own offset.
func scanBoxes(r io.ReadSeeker, offset, end uint64, depth int, lim Limits, count *int, track model.TrackRef) ([]*Box, error) {
	var boxes []*Box
	for offset < end {
		if end-offset < mp4.SmallHeaderSize {
			// Trailing zero padding is legal at the end of a container.
			tail := make([]byte, end-offset)
			if _, err := r.Seek(int64(offset), io.SeekStart); err != nil {
				return nil, err
			}
			if _, err := io.ReadFull(r, tail); err != nil {
				return nil, model.WrapError(model.StageIngest, "scan", track, offset, "unreadable trailing bytes", err)
			}
			zero := true
			for _, b := range tail {
				if b != 0 {
					zero = false
					break
				}
			}
			if zero {
				return boxes, nil
			}
			return nil, model.NewError(model.StageIngest, "scan", track, offset, "truncated box header")
		}
		if _, err := r.Seek(int64(offset), io.SeekStart); err != nil {
			return nil, err
		}
		bi, err := mp4.ReadBoxInfo(r)
		if err != nil {
			return nil, model.WrapError(model.StageIngest, "scan", track, offset, "unreadable box header", err)
		}
		*count++
		if *count > lim.MaxBoxes {
			return nil, model.NewError(model.StageIngest, "scan", track, offset, "box count exceeds budget")
		}
		if bi.Size < bi.HeaderSize {
			return nil, model.Errorf(model.StageIngest, "scan", track, offset, "box %s size %d smaller than header %d", bi.Type.String(), bi.Size, bi.HeaderSize)
		}
		if bi.Size > end-offset {
			return nil, model.Errorf(model.StageIngest, "scan", track, offset, "truncated box %s: declared size %d exceeds available %d", bi.Type.String(), bi.Size, end-offset)
		}
		node := &Box{Offset: offset, Size: bi.Size, HeaderSize: bi.HeaderSize, Type: bi.Type.String()}
		if containerTypes[node.Type] {
			if depth+1 > lim.MaxDepth {
				return nil, model.Errorf(model.StageIngest, "scan", track, offset, "container depth exceeds budget at %s", node.Type)
			}
			children, err := scanBoxes(r, node.PayloadOffset(), node.Offset+node.Size, depth+1, lim, count, track)
			if err != nil {
				return nil, err
			}
			node.Children = children
		}
		boxes = append(boxes, node)
		offset += bi.Size
	}
	return boxes, nil
}

// openContainer opens path, enforces the byte budget and scans the box tree.
func openContainer(path string, lim Limits) (*os.File, *Container, error) {
	ref := model.TrackRef{Name: filepath.Base(path)}
	st, err := os.Stat(path)
	if err != nil {
		return nil, nil, model.WrapError(model.StageIngest, "open", ref, 0, "cannot stat input", err)
	}
	if !st.Mode().IsRegular() {
		return nil, nil, model.NewError(model.StageIngest, "open", ref, 0, "input is not a regular file")
	}
	if st.Size() > lim.MaxBytes {
		return nil, nil, model.Errorf(model.StageIngest, "open", ref, 0, "input size %d exceeds budget %d", st.Size(), lim.MaxBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, model.WrapError(model.StageIngest, "open", ref, 0, "cannot open input", err)
	}
	digest, err := digestFile(f)
	if err != nil {
		f.Close()
		return nil, nil, model.WrapError(model.StageIngest, "digest", ref, 0, "cannot hash input", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, nil, err
	}
	count := 0
	tree, err := scanBoxes(f, 0, uint64(st.Size()), 0, lim, &count, ref)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	c := &Container{
		Path:     path,
		Size:     uint64(st.Size()),
		Digest:   digest,
		BoxCount: count,
		Tree:     &Box{Type: "root", Size: uint64(st.Size()), Children: tree},
	}
	return f, c, nil
}

// fileSeeker adapts an *os.File to the payload reader used by go-mp4.
type payloadReader struct {
	r   io.ReadSeeker
	box *Box
}

func (p payloadReader) ReadPayload(dst any) error {
	if _, err := p.r.Seek(int64(p.box.PayloadOffset()), io.SeekStart); err != nil {
		return err
	}
	n, err := mp4.Unmarshal(p.r, p.box.PayloadSize(), dst.(mp4.IBox), mp4.Context{})
	if err != nil {
		return fmt.Errorf("box %s at %d: %w", p.box.Type, p.box.Offset, err)
	}
	if n > p.box.PayloadSize() {
		return fmt.Errorf("box %s at %d overran payload", p.box.Type, p.box.Offset)
	}
	return nil
}
