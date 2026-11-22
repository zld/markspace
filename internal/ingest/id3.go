package ingest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
)

// ID3Frame is one decoded ID3v2 frame.
type ID3Frame struct {
	ID    string
	Text  string
	Descr string
}

// ID3Tag is a decoded ID3v2 tag.
type ID3Tag struct {
	Version uint8
	Frames  []ID3Frame
}

// Text returns the first frame with the given ID.
func (t ID3Tag) Text(id string) (string, bool) {
	for _, f := range t.Frames {
		if f.ID == id {
			return f.Text, true
		}
	}
	return "", false
}

// ParseID3 decodes an ID3v2.2/2.3/2.4 tag from the start of payload. A tag
// with a malformed frame is still returned; err reports the first bad frame.
func ParseID3(payload []byte) (ID3Tag, error) {
	var tag ID3Tag
	if len(payload) < 10 || string(payload[0:3]) != "ID3" {
		return tag, fmt.Errorf("missing ID3 header")
	}
	tag.Version = payload[3]
	if tag.Version < 2 || tag.Version > 4 {
		return tag, fmt.Errorf("unsupported ID3 major version %d", tag.Version)
	}
	size, err := syncSafe(payload[6:10])
	if err != nil {
		return tag, fmt.Errorf("bad ID3 size: %w", err)
	}
	end := 10 + size
	if end > len(payload) || end < 10 {
		end = len(payload)
	}
	body := payload[10:end]
	var firstErr error
	for len(body) >= 10 && !allZero(body[:4]) {
		id := string(body[:4])
		var fsize int
		if tag.Version == 4 {
			fsize, err = syncSafe(body[4:8])
		} else {
			fsize = int(binary.BigEndian.Uint32(body[4:8]))
		}
		if err != nil || fsize < 0 || 10+fsize > len(body) {
			if firstErr == nil {
				firstErr = fmt.Errorf("frame %q has invalid size %d", id, fsize)
			}
			break
		}
		data := body[10 : 10+fsize]
		if text, descr, ok := decodeTextFrame(id, data); ok {
			tag.Frames = append(tag.Frames, ID3Frame{ID: id, Text: text, Descr: descr})
		}
		body = body[10+fsize:]
	}
	return tag, firstErr
}

func syncSafe(b []byte) (int, error) {
	if len(b) < 4 {
		return 0, fmt.Errorf("need 4 bytes")
	}
	for _, v := range b[:4] {
		if v&0x80 != 0 {
			return 0, fmt.Errorf("not a syncsafe integer")
		}
	}
	return int(b[0])<<21 | int(b[1])<<14 | int(b[2])<<7 | int(b[3]), nil
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

func decodeTextFrame(id string, data []byte) (text, descr string, ok bool) {
	if len(data) == 0 {
		return "", "", false
	}
	switch id[0] {
	case 'T':
	case 'W':
		return strings.TrimRight(string(data), "\x00"), "", true
	default:
		return "", "", false
	}
	enc := data[0]
	raw := data[1:]
	switch enc {
	case 0:
		text = strings.TrimRight(string(raw), "\x00")
	case 3:
		text = strings.TrimRight(string(raw), "\x00")
	case 1, 2:
		text = decodeUTF16(raw, enc == 1)
	default:
		return "", "", false
	}
	if id == "TXXX" {
		parts := strings.SplitN(text, "\x00", 2)
		if len(parts) == 2 {
			return parts[1], parts[0], true
		}
		return text, "", true
	}
	return text, "", true
}

func decodeUTF16(raw []byte, bom bool) string {
	be := true
	if bom && len(raw) >= 2 {
		if raw[0] == 0xFF && raw[1] == 0xFE {
			be = false
			raw = raw[2:]
		} else if raw[0] == 0xFE && raw[1] == 0xFF {
			raw = raw[2:]
		}
	}
	var runes []rune
	for i := 0; i+1 < len(raw); i += 2 {
		var u uint16
		if be {
			u = uint16(raw[i])<<8 | uint16(raw[i+1])
		} else {
			u = uint16(raw[i+1])<<8 | uint16(raw[i])
		}
		if u == 0 {
			break
		}
		runes = append(runes, rune(u))
	}
	return string(runes)
}

// Emsg is a decoded DASH event message box.
type Emsg struct {
	Version   uint8
	SchemeID  string
	Value     string
	Timescale uint32
	TimeTicks int64
	Duration  uint32
	ID        uint32
	Message   []byte
}

// ParseEmsg decodes an emsg payload (version 0 or 1).
func ParseEmsg(payload []byte) (Emsg, error) {
	var e Emsg
	if len(payload) < 4 {
		return e, fmt.Errorf("emsg too short")
	}
	e.Version = payload[0]
	body := payload[4:]
	readStr := func() (string, error) {
		i := bytes.IndexByte(body, 0)
		if i < 0 {
			return "", fmt.Errorf("unterminated emsg string")
		}
		s := string(body[:i])
		body = body[i+1:]
		return s, nil
	}
	if e.Version == 0 {
		var err error
		if e.SchemeID, err = readStr(); err != nil {
			return e, err
		}
		if e.Value, err = readStr(); err != nil {
			return e, err
		}
		if len(body) < 16 {
			return e, fmt.Errorf("emsg v0 truncated")
		}
		e.Timescale = binary.BigEndian.Uint32(body[0:4])
		e.TimeTicks = int64(binary.BigEndian.Uint32(body[4:8]))
		e.Duration = binary.BigEndian.Uint32(body[8:12])
		e.ID = binary.BigEndian.Uint32(body[12:16])
		e.Message = body[16:]
		return e, nil
	}
	if e.Version == 1 {
		if len(body) < 20 {
			return e, fmt.Errorf("emsg v1 truncated")
		}
		e.Timescale = binary.BigEndian.Uint32(body[0:4])
		e.TimeTicks = int64(binary.BigEndian.Uint64(body[4:12]))
		e.Duration = binary.BigEndian.Uint32(body[12:16])
		e.ID = binary.BigEndian.Uint32(body[16:20])
		body = body[20:]
		var err error
		if e.SchemeID, err = readStr(); err != nil {
			return e, err
		}
		if e.Value, err = readStr(); err != nil {
			return e, err
		}
		e.Message = body
		return e, nil
	}
	return e, fmt.Errorf("unsupported emsg version %d", e.Version)
}
