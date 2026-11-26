package cmaf

import (
	"encoding/binary"
	"errors"
	"io"
	"os"

	"example.com/archiveverify/internal/model"
)

// InitTrack contains the track-level timing information needed by fragments.
type InitTrack struct {
	ID              uint32 `json:"id"`
	Handler         string `json:"handler"`
	TimeScale       uint32 `json:"timescale"`
	DefaultDuration uint32 `json:"default_duration"`
	DefaultSize     uint32 `json:"default_size"`
	DefaultFlags    uint32 `json:"default_flags"`
}

// Init is a parsed CMAF initialization segment.
type Init struct {
	Path   string      `json:"path"`
	Tracks []InitTrack `json:"tracks"`
}

// Track returns the track description by ID.
func (in *Init) Track(id uint32) *InitTrack {
	if in == nil {
		return nil
	}
	for i := range in.Tracks {
		if in.Tracks[i].ID == id {
			return &in.Tracks[i]
		}
	}
	return nil
}

// ParseInitFile parses tkhd/mdhd/hdlr/trex from an init segment.
func ParseInitFile(path string) (*Init, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, model.NewParseError(path, "", -1, -1, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, model.NewParseError(path, "", -1, -1, err)
	}
	return ParseInit(f, path, st.Size(), MaxBoxSize)
}

// ParseInit parses init metadata from a ReaderAt.
func ParseInit(r io.ReaderAt, source string, size, maxBox int64) (*Init, error) {
	boxes, err := ScanBoxes(r, source, size, maxBox)
	if err != nil {
		return nil, err
	}
	var moov *BoxHeader
	for i := range boxes {
		if boxes[i].Type == "moov" {
			moov = &boxes[i]
			break
		}
	}
	if moov == nil {
		return nil, model.NewParseError(source, "moov", 0, 0, errors.New("moov not found"))
	}
	moovKids, err := ScanRange(r, source, moov.PayloadOffset, moov.End, maxBox)
	if err != nil {
		return nil, err
	}
	out := &Init{}
	defaults := map[uint32]InitTrack{}
	for _, b := range moovKids {
		switch b.Type {
		case "trak":
			t, err := parseTRAK(r, source, b, maxBox)
			if err != nil {
				return nil, err
			}
			out.Tracks = append(out.Tracks, t)
		case "mvex":
			mvexKids, err := ScanRange(r, source, b.PayloadOffset, b.End, maxBox)
			if err != nil {
				return nil, err
			}
			for _, tb := range mvexKids {
				if tb.Type != "trex" {
					continue
				}
				id, d, sz, fl, err := parseTREX(r, source, tb)
				if err != nil {
					return nil, err
				}
				defaults[id] = InitTrack{ID: id, DefaultDuration: d, DefaultSize: sz, DefaultFlags: fl}
			}
		}
	}
	for i := range out.Tracks {
		if d, ok := defaults[out.Tracks[i].ID]; ok {
			out.Tracks[i].DefaultDuration = d.DefaultDuration
			out.Tracks[i].DefaultSize = d.DefaultSize
			out.Tracks[i].DefaultFlags = d.DefaultFlags
		}
	}
	if len(out.Tracks) != 2 {
		return nil, model.NewParseError(source, "trak", moov.Offset, moov.End,
			errors.New("expected exactly two tracks"))
	}
	return out, nil
}

func parseTRAK(r io.ReaderAt, source string, trak BoxHeader, maxBox int64) (InitTrack, error) {
	kids, err := ScanRange(r, source, trak.PayloadOffset, trak.End, maxBox)
	if err != nil {
		return InitTrack{}, err
	}
	var t InitTrack
	var mdia *BoxHeader
	for _, b := range kids {
		switch b.Type {
		case "tkhd":
			id, err := parseTKHD(r, b)
			if err != nil {
				return InitTrack{}, model.NewParseError(source, "tkhd", b.Offset, b.PayloadOffset, err)
			}
			t.ID = id
		case "mdia":
			b := b
			mdia = &b
		}
	}
	if mdia == nil {
		return InitTrack{}, model.NewParseError(source, "mdia", trak.Offset, trak.End, errors.New("mdia missing"))
	}
	mkids, err := ScanRange(r, source, mdia.PayloadOffset, mdia.End, maxBox)
	if err != nil {
		return InitTrack{}, err
	}
	for _, b := range mkids {
		switch b.Type {
		case "mdhd":
			ts, err := parseMDHDTimescale(r, b)
			if err != nil {
				return t, model.NewParseError(source, "mdhd", b.Offset, b.PayloadOffset, err)
			}
			t.TimeScale = ts
		case "hdlr":
			if h, err := parseHDLR(r, b); err == nil {
				t.Handler = h
			}
		}
	}
	if t.ID == 0 || t.TimeScale == 0 {
		return t, model.NewParseError(source, "trak", trak.Offset, trak.End,
			errors.New("track ID or timescale missing"))
	}
	return t, nil
}

func boxPayload(r io.ReaderAt, b BoxHeader, n int) ([]byte, error) {
	buf := make([]byte, n)
	_, err := r.ReadAt(buf, b.PayloadOffset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf, nil
}

func parseTKHD(r io.ReaderAt, b BoxHeader) (uint32, error) {
	buf, err := boxPayload(r, b, 24)
	if err != nil || len(buf) < 24 {
		return 0, io.ErrUnexpectedEOF
	}
	if buf[0] == 1 {
		return binary.BigEndian.Uint32(buf[20:24]), nil
	}
	return binary.BigEndian.Uint32(buf[12:16]), nil
}

func parseMDHDTimescale(r io.ReaderAt, b BoxHeader) (uint32, error) {
	buf, err := boxPayload(r, b, 24)
	if err != nil || len(buf) < 24 {
		return 0, io.ErrUnexpectedEOF
	}
	if buf[0] == 1 {
		return binary.BigEndian.Uint32(buf[20:24]), nil
	}
	return binary.BigEndian.Uint32(buf[12:16]), nil
}

func parseHDLR(r io.ReaderAt, b BoxHeader) (string, error) {
	buf, err := boxPayload(r, b, 12)
	if err != nil || len(buf) < 12 {
		return "", io.ErrUnexpectedEOF
	}
	return string(buf[8:12]), nil
}

func parseTREX(r io.ReaderAt, source string, b BoxHeader) (uint32, uint32, uint32, uint32, error) {
	buf, err := boxPayload(r, b, 24)
	if err != nil || len(buf) < 24 {
		return 0, 0, 0, 0, model.NewParseError(source, "trex", b.Offset, b.PayloadOffset, io.ErrUnexpectedEOF)
	}
	return binary.BigEndian.Uint32(buf[4:8]),
		binary.BigEndian.Uint32(buf[12:16]),
		binary.BigEndian.Uint32(buf[16:20]),
		binary.BigEndian.Uint32(buf[20:24]), nil
}
