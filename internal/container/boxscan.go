package container

import (
	"encoding/binary"
	"fmt"

	"replaycheck/internal/clock"
)

type boxHeader struct {
	typ                     string
	start, headerSize, size int64
}

type preflightInfo struct {
	brand      string
	moovOffset int64
	tracks     []preflightTrack
}

type preflightTrack struct {
	trackID    uint32
	trakOffset int64
	stblOffset int64
}

func readBoxHeader(data []byte, off int64) (boxHeader, *clock.Error) {
	if off < 0 || off+8 > int64(len(data)) {
		return boxHeader{}, clock.NewError(clock.CodeTruncated, "box header is truncated", int64(len(data)))
	}
	start := off
	size32 := binary.BigEndian.Uint32(data[off : off+4])
	typ := string(data[off+4 : off+8])
	hdr := int64(8)
	size := int64(size32)
	if size32 == 1 {
		if off+16 > int64(len(data)) {
			return boxHeader{}, clock.NewError(clock.CodeTruncated, "64-bit box size is truncated", off)
		}
		size = int64(binary.BigEndian.Uint64(data[off+8 : off+16]))
		hdr = 16
	} else if size32 == 0 {
		size = int64(len(data)) - off
	}
	if size < hdr {
		return boxHeader{}, clock.NewError(clock.CodeBadHeader, fmt.Sprintf("box %q has invalid size %d", typ, size), off)
	}
	if off+size > int64(len(data)) {
		return boxHeader{}, clock.NewError(clock.CodeTruncated, fmt.Sprintf("box %q declares end %d beyond file %d", typ, off+size, len(data)), off)
	}
	return boxHeader{typ: typ, start: start, headerSize: hdr, size: size}, nil
}

func childBoxes(data []byte, b boxHeader) ([]boxHeader, *clock.Error) {
	start := b.start + b.headerSize
	end := b.start + b.size
	out := []boxHeader{}
	for off := start; off < end; {
		h, err := readBoxHeader(data, off)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
		off = h.start + h.size
	}
	return out, nil
}

func findChild(data []byte, b boxHeader, typ string) (boxHeader, bool, *clock.Error) {
	children, err := childBoxes(data, b)
	if err != nil {
		return boxHeader{}, false, err
	}
	for _, c := range children {
		if c.typ == typ {
			return c, true, nil
		}
	}
	return boxHeader{}, false, nil
}

func preflight(data []byte) (*preflightInfo, *clock.Error) {
	if len(data) < 8 {
		return nil, clock.NewError(clock.CodeTruncated, "MP4/MOV header is shorter than 8 bytes", int64(len(data)))
	}
	ftyp, err := readBoxHeader(data, 0)
	if err != nil {
		return nil, err
	}
	if ftyp.typ != "ftyp" {
		return nil, clock.NewError(clock.CodeBadHeader, "missing ftyp MP4/MOV header", 4)
	}
	info := &preflightInfo{moovOffset: -1}
	if len(data) >= 12 {
		info.brand = string(data[8:12])
	}

	top, err := childBoxes(data, boxHeader{typ: "root", start: 0, headerSize: 0, size: int64(len(data))})
	if err != nil {
		return nil, err
	}
	var moov boxHeader
	foundMoov := false
	for _, b := range top {
		if b.typ == "moov" {
			moov, foundMoov = b, true
		}
	}
	if !foundMoov {
		return nil, clock.NewError(clock.CodeMissingTrack, "moov box not found", int64(len(data)))
	}
	info.moovOffset = moov.start

	traks, perr := childBoxes(data, moov)
	if perr != nil {
		return nil, perr
	}
	for _, trak := range traks {
		if trak.typ != "trak" {
			continue
		}
		track := preflightTrack{trakOffset: trak.start, stblOffset: -1}
		mdia, ok, perr := findChild(data, trak, "mdia")
		if perr != nil {
			return nil, perr
		}
		if ok {
			minf, ok, perr := findChild(data, mdia, "minf")
			if perr != nil {
				return nil, perr
			}
			if ok {
				stbl, ok, perr := findChild(data, minf, "stbl")
				if perr != nil {
					return nil, perr
				}
				if ok {
					track.stblOffset = stbl.start
				}
			}
		}
		if track.stblOffset < 0 {
			return nil, clock.NewError(clock.CodeMissingTrack, "trak is missing mdia/minf/stbl track table", trak.start)
		}
		info.tracks = append(info.tracks, track)
	}
	if len(info.tracks) == 0 {
		return nil, clock.NewError(clock.CodeMissingTrack, "moov contains no trak boxes", moov.start)
	}
	return info, nil
}
