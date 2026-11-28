// Package container parses MP4/MOV files into normalized tracks and samples.
// Parsing is delegated to github.com/abema/go-mp4; this package adds the
// mediasync validation rules: bounded resources, overflow-checked offsets and
// faults that always carry the original byte offset inside the input.
package container

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"mediasync/internal/fault"
)

// File is the validated container-level view.
type File struct {
	Size       uint64
	Timescale  uint32 // movie timescale from mvhd
	MdatOffset int64  // first mdat box payload offset, -1 if absent
	MdatSize   uint64
	Tracks     []*Track
}

// Parser is single-use: call Parse once.
type Parser struct {
	ctx      context.Context
	f        *os.File
	size     uint64
	cur      *Track
	out      *File
	seenFtyp bool
	seenMoov bool
}

// Parse opens, bounds and parses one MP4/MOV file. The returned File is nil
// when an error is returned.
func Parse(ctx context.Context, path string) (*File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Size() > MaxFileSize {
		return nil, fault.New(fault.ErrLimitExceeded, "", 0,
			"file size %d exceeds limit %d", info.Size(), MaxFileSize)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	p := &Parser{
		ctx:  ctx,
		f:    f,
		size: uint64(info.Size()),
		out:  &File{Size: uint64(info.Size()), MdatOffset: -1},
	}
	if _, err := mp4.ReadBoxStructure(f, p.handler); err != nil {
		return nil, p.translate(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fault.New(fault.ErrCanceled, "", -1, "canceled while parsing")
	}
	if !p.seenFtyp {
		return nil, fault.New(fault.ErrContainerDecl, "", 0, "missing ftyp box")
	}
	if !p.seenMoov {
		return nil, fault.New(fault.ErrContainerDecl, "", 0, "missing moov box")
	}
	if p.out.MdatOffset < 0 {
		return nil, fault.New(fault.ErrContainerDecl, "", 0, "missing mdat box")
	}
	if len(p.out.Tracks) == 0 {
		return nil, fault.New(fault.ErrContainerDecl, "", -1, "no tracks found")
	}
	return p.out, nil
}

// translate maps third-party parser errors onto the unified fault model while
// retaining an offset whenever the parser gives us one.
func (p *Parser) translate(err error) error {
	if f, ok := fault.AsFault(err); ok {
		return f
	}
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return fault.New(fault.ErrTruncated, "", -1, "container ends unexpectedly: %v", err)
	}
	return fault.New(fault.ErrTruncated, "", -1, "container parse failed: %v", err)
}

func (p *Parser) handler(h *mp4.ReadHandle) (interface{}, error) {
	if err := p.ctx.Err(); err != nil {
		return nil, fault.New(fault.ErrCanceled, "", -1, "canceled while parsing")
	}
	bi := h.BoxInfo
	// A box whose declared size runs past end of file is a truncated
	// container; out-of-bounds faults are reserved for table data.
	if bi.Offset > p.size || bi.Size > p.size-bi.Offset {
		return nil, fault.New(fault.ErrTruncated, "", int64(bi.Offset),
			"box %q declared size %d but only %d bytes remain",
			bi.Type.String(), bi.Size, p.size-bi.Offset)
	}
	trackName := ""
	if p.cur != nil {
		trackName = p.cur.Name
	}

	switch bi.Type {
	case mp4.BoxTypeFtyp():
		p.seenFtyp = true
		return h.Expand()

	case mp4.BoxTypeMvhd():
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, p.translate(err)
		}
		mvhd := box.(*mp4.Mvhd)
		if mvhd.Timescale == 0 {
			return nil, fault.New(fault.ErrIllegalTimebase, "", int64(bi.Offset),
				"mvhd timescale is zero")
		}
		p.out.Timescale = mvhd.Timescale
		return nil, nil

	case mp4.BoxTypeMdat():
		// Record data region and validate it against the file boundary.
		payloadOff, err := addOffset(bi.Offset, bi.HeaderSize, p.size)
		if err != nil {
			return nil, err
		}
		payloadSize := bi.Size - bi.HeaderSize
		if _, err := addOffset(payloadOff, payloadSize, p.size); err != nil {
			return nil, err
		}
		if p.out.MdatOffset < 0 {
			p.out.MdatOffset = int64(payloadOff)
			p.out.MdatSize = payloadSize
		}
		return nil, nil

	case mp4.BoxTypeMoov():
		p.seenMoov = true
		vals, err := h.Expand()
		if err != nil {
			return nil, err
		}
		if len(p.out.Tracks) > MaxTracks {
			return nil, fault.New(fault.ErrLimitExceeded, "", int64(bi.Offset),
				"track count %d exceeds limit %d", len(p.out.Tracks), MaxTracks)
		}
		return vals, nil

	case mp4.BoxTypeTrak():
		if len(p.out.Tracks) >= MaxTracks {
			return nil, fault.New(fault.ErrLimitExceeded, "", int64(bi.Offset),
				"track count exceeds limit %d", MaxTracks)
		}
		p.cur = newTrack(int64(bi.Offset))
		vals, err := h.Expand()
		if err != nil {
			return nil, err
		}
		if err := p.cur.finalize(p.out, int64(bi.Offset)); err != nil {
			return nil, err
		}
		p.out.Tracks = append(p.out.Tracks, p.cur)
		p.cur = nil
		return vals, nil

	default:
		if p.cur == nil {
			// Skip unsupported top-level boxes cleanly.
			if len(h.Path) <= 1 {
				return h.Expand()
			}
			return nil, nil
		}
		if err := p.cur.handleBox(p, h, &bi, trackName); err != nil {
			return nil, err
		}
		return h.Expand()
	}
}
