package media

import (
	"encoding/binary"
	"io"
	"strings"

	"github.com/abema/go-mp4"
)

func parseMovie(r io.ReadSeeker, size int64) (*rawMovie, error) {
	m := &rawMovie{}
	var cur *rawTrack
	err := walkBoxes(r, 0, size, 0, func(path []string, b Box) error {
		parent := ""
		if len(path) >= 2 {
			parent = path[len(path)-2]
		}
		if parent != "" && allowedChildren[parent] != nil && !allowedChild(parent, b.Type) {
			return NewError(ClassUnsupported, "parse."+parent, b.Offset, "unsupported box %q inside %q", b.Type, parent)
		}
		switch b.Type {
		case "ftyp":
			v := &mp4.Ftyp{}
			if err := unmarshalBox(r, b, v); err != nil {
				return err
			}
			m.Ftyp = v
		case "mvhd":
			v := &mp4.Mvhd{}
			if err := unmarshalBox(r, b, v); err != nil {
				return err
			}
			m.Mvhd = v
		case "trak":
			cur = &rawTrack{Trak: b}
			m.Tracks = append(m.Tracks, cur)
		case "tkhd":
			v := &mp4.Tkhd{}
			if err := unmarshalBox(r, b, v); err != nil {
				return err
			}
			cur.Tkhd = v
		case "mdhd":
			v := &mp4.Mdhd{}
			if err := unmarshalBox(r, b, v); err != nil {
				return err
			}
			cur.Mdhd = v
		case "hdlr":
			v := &mp4.Hdlr{}
			if err := unmarshalBox(r, b, v); err != nil {
				return err
			}
			cur.Hdlr = v
		case "elst":
			v := &mp4.Elst{}
			if err := unmarshalBox(r, b, v); err != nil {
				return err
			}
			cur.Elst = v
		case "stsd":
			if cur == nil {
				return NewError(ClassBoxBounds, "parse.stsd", b.Offset, "stsd outside trak")
			}
			codec, entry, err := readSampleDesc(r, b)
			if err != nil {
				return err
			}
			cur.Stsd = b
			cur.Codec = codec
			cur.Entry = entry
		case "stts":
			v := &mp4.Stts{}
			if err := unmarshalBox(r, b, v); err != nil {
				return err
			}
			cur.Stts = v
			cur.SttsBox = b
		case "ctts":
			v := &mp4.Ctts{}
			if err := unmarshalBox(r, b, v); err != nil {
				return err
			}
			cur.Ctts = v
			cur.CttsBox = b
		case "stsc":
			v := &mp4.Stsc{}
			if err := unmarshalBox(r, b, v); err != nil {
				return err
			}
			cur.Stsc = v
			cur.StscBox = b
		case "stsz":
			v := &mp4.Stsz{}
			if err := unmarshalBox(r, b, v); err != nil {
				return err
			}
			cur.Stsz = v
			cur.StszBox = b
		case "stco":
			v := &mp4.Stco{}
			if err := unmarshalBox(r, b, v); err != nil {
				return err
			}
			cur.Stco = v
			cur.StcoBox = b
		case "co64":
			v := &mp4.Co64{}
			if err := unmarshalBox(r, b, v); err != nil {
				return err
			}
			cur.Co64 = v
			cur.Co64Box = b
		case "stss":
			v := &mp4.Stss{}
			if err := unmarshalBox(r, b, v); err != nil {
				return err
			}
			cur.Stss = v
			cur.StssBox = b
		case "mdat":
			m.Mdat = append(m.Mdat, b)
		}
		if b.Type == "url " && parent == "dref" {
			v := &mp4.Url{}
			if err := unmarshalBox(r, b, v); err != nil {
				return err
			}
			if v.GetFlags()&mp4.UrlSelfContained == 0 {
				return NewError(ClassUnsupported, "parse.url", b.Offset, "non self-contained data reference is not supported")
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if m.Mvhd == nil {
		return nil, NewError(ClassUnsupported, "parse", 0, "mvhd box missing")
	}
	if len(m.Tracks) == 0 {
		return nil, NewError(ClassMissingTrack, "parse", 0, "no tracks in moov")
	}
	for _, t := range m.Tracks {
		if err := requireTrackBoxes(t); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// walkBoxes visits every box under [start,end), validating bounds and
// recursing only into known structural containers.
func walkBoxes(r io.ReadSeeker, start, end int64, depth int, fn func(path []string, b Box) error) error {
	if depth > 8 {
		return NewError(ClassUnsupported, "walk", start, "box nesting exceeds 8 levels")
	}
	return walkBoxesPath(r, start, end, depth, nil, fn)
}

func walkBoxesPath(r io.ReadSeeker, start, end int64, depth int, path []string, fn func([]string, Box) error) error {
	boxes, err := scanBoxList(r, start, end, depth)
	if err != nil {
		return err
	}
	for _, b := range boxes {
		p := append(append([]string(nil), path...), b.Type)
		if err := fn(p, b); err != nil {
			return err
		}
		if boxContainers[b.Type] {
			skip := int64(0)
			if b.Type == "dref" {
				skip = 8 // full-box version/flags plus entry count
			}
			if err := walkBoxesPath(r, b.Offset+b.Header+skip, b.Offset+b.Size, depth+1, p, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

func unmarshalBox(r io.ReadSeeker, b Box, dst mp4.IBox) error {
	if _, err := r.Seek(b.Offset+b.Header, io.SeekStart); err != nil {
		return WrapError(ClassIO, "parse."+b.Type, b.Offset, err, "seek failed")
	}
	if b.Payload > MaxInputSize {
		return NewError(ClassSizeOverflow, "parse."+b.Type, b.Offset, "payload %d exceeds limit", b.Payload)
	}
	if _, err := mp4.Unmarshal(r, uint64(b.Payload), dst, mp4.Context{}); err != nil {
		return classifyMP4(err, "parse."+b.Type, b.Offset)
	}
	return nil
}

// readSampleDesc validates the stsd entry and returns the codec name.
func readSampleDesc(r io.ReadSeeker, b Box) (codec, entry string, err error) {
	if b.Payload < 16 {
		return "", "", NewError(ClassTruncated, "parse.stsd", b.Offset, "stsd payload too small")
	}
	hdr := make([]byte, 16)
	if _, err := r.Seek(b.Offset+b.Header, io.SeekStart); err != nil {
		return "", "", WrapError(ClassIO, "parse.stsd", b.Offset, err, "seek failed")
	}
	if _, err := io.ReadFull(r, hdr); err != nil {
		return "", "", WrapError(ClassTruncated, "parse.stsd", b.Offset, err, "short stsd header")
	}
	count := binary.BigEndian.Uint32(hdr[4:8])
	if count != 1 {
		return "", "", NewError(ClassUnsupported, "parse.stsd", b.Offset, "expected exactly 1 sample description, got %d", count)
	}
	entrySize := int64(binary.BigEndian.Uint32(hdr[8:12]))
	entry = string(hdr[12:16])
	if entrySize < 16 || entrySize > b.Payload-8 {
		return "", "", NewError(ClassBoxBounds, "parse.stsd", b.Offset+8, "sample entry %q size %d out of bounds", entry, entrySize)
	}
	switch entry {
	case "avc1":
		codec = "avc1"
	case "mp4a":
		codec = "mp4a"
	case "encv", "enca":
		return "", "", NewError(ClassUnsupported, "parse.stsd", b.Offset+8, "encrypted sample entry %q is not supported", entry)
	default:
		return "", "", NewError(ClassUnsupported, "parse.stsd", b.Offset+8, "unsupported sample entry %q", entry)
	}
	return codec, entry, nil
}

// classifyMP4 maps go-mp4 library errors onto the shared taxonomy so callers
// see stable categories instead of library-specific strings.
func classifyMP4(err error, op string, offset int64) error {
	if err == nil {
		return nil
	}
	if _, ok := AsError(err); ok {
		return err
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "too large box size"):
		return WrapError(ClassBoxBounds, op, offset, err, "box exceeds container")
	case strings.Contains(msg, "unexpected EOF"), strings.Contains(msg, "EOF"):
		return WrapError(ClassTruncated, op, offset, err, "unexpected end of input")
	case strings.Contains(msg, "invalid size"):
		return WrapError(ClassBoxBounds, op, offset, err, "invalid box size")
	case strings.Contains(msg, "box info not found"),
		strings.Contains(msg, "unsupported box version"),
		strings.Contains(msg, "not supported"):
		return WrapError(ClassUnsupported, op, offset, err, "unsupported box")
	case strings.Contains(msg, "invalid alignment"), strings.Contains(msg, "too few elements"):
		return WrapError(ClassBoxBounds, op, offset, err, "box payload truncated")
	default:
		return WrapError(ClassIO, op, offset, err, "mp4 parse failed")
	}
}

func languageString(lang [3]byte) string {
	var sb strings.Builder
	for _, c := range lang {
		ch := c + 0x60
		if ch < 'a' || ch > 'z' {
			ch = '?'
		}
		sb.WriteByte(ch)
	}
	return sb.String()
}

func handlerName(h *mp4.Hdlr) string {
	if h == nil {
		return ""
	}
	return string(h.HandlerType[:])
}
