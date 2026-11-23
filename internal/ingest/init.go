package ingest

import (
	"fmt"

	"github.com/abema/go-mp4"

	"mediacore/internal/fault"
)

// InitTrack is one track declared by the initialization segment.
type InitTrack struct {
	TrackID     uint32 `json:"track_id"`
	Kind        string `json:"kind"`
	HandlerType string `json:"handler_type"`
	Timescale   uint32 `json:"timescale"`
	Language    string `json:"language,omitempty"`
	Width       uint32 `json:"width,omitempty"`
	Height      uint32 `json:"height,omitempty"`
}

// InitInfo summarizes a validated initialization segment.
type InitInfo struct {
	File           string      `json:"file"`
	Brands         []string    `json:"brands"`
	MovieTimescale uint32      `json:"movie_timescale"`
	Tracks         []InitTrack `json:"tracks"`
	MoovOffset     int64       `json:"moov_offset"`
	MoovSize       int64       `json:"moov_size"`
}

// ParseInit reads ftyp and moov from the start of the input. All reads are
// bounded by declared box sizes; a malformed tree is reported with the raw
// offset of the offending box.
func ParseInit(r *Reader) (*InitInfo, *fault.Error) {
	h, ok, f := r.Next(0)
	if f != nil {
		return nil, f
	}
	if !ok {
		return nil, fault.New(fault.CodeMissingField, r.File, 0, "", "empty input")
	}
	if h.Type != "ftyp" {
		return nil, fault.New(fault.CodeMissingField, r.File, 0, h.Type, "initialization segment must start with ftyp")
	}
	var ftyp mp4.Ftyp
	if f := r.Unmarshal(h, &ftyp); f != nil {
		return nil, f
	}
	info := &InitInfo{File: r.File, Brands: []string{string(ftyp.MajorBrand[:])}}
	for _, b := range ftyp.CompatibleBrands {
		info.Brands = append(info.Brands, string(b.CompatibleBrand[:]))
	}
	next, ok, f := r.Next(h.End())
	if f != nil {
		return nil, f
	}
	if !ok || next.Type != "moov" {
		return nil, fault.New(fault.CodeMissingField, r.File, h.End(), "", "moov box must follow ftyp")
	}
	info.MoovOffset, info.MoovSize = next.Offset, next.Size

	var mvhd mp4.Mvhd
	foundMvhd := false
	err := r.walkChildren(next, func(child Header) *fault.Error {
		switch child.Type {
		case "mvhd":
			if f := r.Unmarshal(child, &mvhd); f != nil {
				return f
			}
			info.MovieTimescale = mvhd.Timescale
			foundMvhd = true
		case "trak":
			track, f := r.parseTrak(child)
			if f != nil {
				return f
			}
			info.Tracks = append(info.Tracks, track)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !foundMvhd {
		return nil, fault.New(fault.CodeMissingField, r.File, next.Offset, "moov", "missing mvhd")
	}
	if info.MovieTimescale == 0 {
		return nil, fault.New(fault.CodeMissingField, r.File, next.Offset, "mvhd", "movie timescale must be positive")
	}
	if len(info.Tracks) == 0 {
		return nil, fault.New(fault.CodeMissingField, r.File, next.Offset, "moov", "no tracks declared")
	}
	return info, nil
}

func (r *Reader) parseTrak(trak Header) (InitTrack, *fault.Error) {
	var out InitTrack
	var mdhd mp4.Mdhd
	var tkhd mp4.Tkhd
	var hdlr mp4.Hdlr
	seenMDHD, seenTKHD, seenHDLR := false, false, false
	err := r.walkChildren(trak, func(child Header) *fault.Error {
		switch child.Type {
		case "tkhd":
			if f := r.Unmarshal(child, &tkhd); f != nil {
				return f
			}
			seenTKHD = true
		case "mdia":
			return r.walkChildren(child, func(md Header) *fault.Error {
				switch md.Type {
				case "mdhd":
					if f := r.Unmarshal(md, &mdhd); f != nil {
						return f
					}
					seenMDHD = true
				case "hdlr":
					if f := r.Unmarshal(md, &hdlr); f != nil {
						return f
					}
					seenHDLR = true
				}
				return nil
			})
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	if !seenTKHD {
		return out, fault.New(fault.CodeMissingField, r.File, trak.Offset, "trak", "missing tkhd")
	}
	if !seenMDHD {
		return out, fault.New(fault.CodeMissingField, r.File, trak.Offset, "trak", "missing mdhd")
	}
	if !seenHDLR {
		return out, fault.New(fault.CodeMissingField, r.File, trak.Offset, "trak", "missing hdlr")
	}
	if mdhd.Timescale == 0 {
		return out, fault.New(fault.CodeMissingField, r.File, trak.Offset, "mdhd", "timescale must be positive")
	}
	out = InitTrack{
		TrackID:     tkhd.TrackID,
		HandlerType: string(hdlr.HandlerType[:]),
		Kind:        kindForHandler(string(hdlr.HandlerType[:])),
		Timescale:   mdhd.Timescale,
		Language:    string(mdhd.Language[:]),
		Width:       tkhd.Width >> 16,
		Height:      tkhd.Height >> 16,
	}
	if out.TrackID == 0 {
		return out, fault.New(fault.CodeMissingField, r.File, trak.Offset, "tkhd", "track id must be positive")
	}
	return out, nil
}

func kindForHandler(handler string) string {
	switch handler {
	case "vide":
		return "video"
	case "soun":
		return "audio"
	default:
		return handler
	}
}

// walkChildren iterates direct children of parent. It reports the first
// malformed child instead of silently skipping it.
func (r *Reader) walkChildren(parent Header, fn func(Header) *fault.Error) *fault.Error {
	if parent.PayloadSize() < 0 {
		return fault.New(fault.CodeBadPayload, r.File, parent.Offset, parent.Type, "negative payload size")
	}
	for off := parent.PayloadStart(); off < parent.End(); {
		h, ok, f := r.Next(off)
		if !ok {
			if f != nil {
				return f
			}
			return fault.New(fault.CodeMissingField, r.File, off, parent.Type, fmt.Sprintf("unexpected end inside %s", parent.Type))
		}
		if h.End() > parent.End() {
			return fault.New(fault.CodeOversizeBox, r.File, h.Offset, h.Type, fmt.Sprintf("child extends past parent %s", parent.Type))
		}
		if f := fn(h); f != nil {
			return f
		}
		off = h.End()
	}
	return nil
}
