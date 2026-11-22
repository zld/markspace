package meta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// ID3v2.4 frame identifiers used by the platform. Only these four frames are
// accepted; anything else is rejected rather than skipped.
const (
	ID3FrameTIT2 = "TIT2"
	ID3FrameTDRC = "TDRC"
	ID3FramePRIV = "PRIV"
	ID3FrameTXXX = "TXXX"
)

// ID3v2.4 header flags.
const id3FlagUnsynchronisation = 0x80

// ErrUnsupportedID3 reports a tag that the restricted profile does not cover.
var ErrUnsupportedID3 = errors.New("meta: unsupported id3 profile")

// ID3Frame is one textual or private frame. Text holds the decoded (or
// unsynchronised) payload; TimestampTicks is set for TDRC at millisecond
// resolution.
type ID3Frame struct {
	ID             string
	Encoding       byte
	Text           string
	TimestampTicks *uint64
	Owner          string
	Private        []byte
}

// ID3Tag is a normalized ID3v2.4 tag.
type ID3Tag struct {
	Frames []ID3Frame
}

func syncSafe(v uint32) int {
	return int((v>>24&0x7f)<<21 | (v>>16&0x7f)<<14 | (v>>8&0x7f)<<7 | v&0x7f)
}

func encodeSyncSafe(n int) [4]byte {
	return [4]byte{byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
}

// EncodeID3 serializes a tag as an ID3v2.4 header plus frames with explicit
// frame sizes (no unsynchronisation, no footer).
func EncodeID3(tag ID3Tag) ([]byte, error) {
	if len(tag.Frames) == 0 || len(tag.Frames) > MaxID3Frames {
		return nil, fmt.Errorf("%w: %d frames", ErrMetaLimit, len(tag.Frames))
	}
	var body bytes.Buffer
	seen := make(map[string]bool)
	for _, f := range tag.Frames {
		if seen[f.ID] {
			return nil, fmt.Errorf("%w: duplicate frame %s", ErrMetaFormat, f.ID)
		}
		seen[f.ID] = true
		var data []byte
		switch f.ID {
		case ID3FrameTIT2, ID3FrameTDRC, ID3FrameTXXX:
			if f.Encoding != 3 {
				return nil, fmt.Errorf("%w: frame %s encoding %d", ErrUnsupportedID3, f.ID, f.Encoding)
			}
			if len(f.Text) > MaxTextLen {
				return nil, fmt.Errorf("%w: frame %s text", ErrMetaLimit, f.ID)
			}
			if f.ID == ID3FrameTXXX && (len(f.Owner) == 0 || len(f.Owner) > 128) {
				return nil, fmt.Errorf("%w: TXXX owner", ErrMetaFormat)
			}
			data = append(data, 3)
			if f.ID == ID3FrameTXXX {
				data = append(data, f.Owner...)
				data = append(data, 0)
			}
			data = append(data, f.Text...)
		case ID3FramePRIV:
			if len(f.Owner) == 0 || len(f.Owner) > 128 {
				return nil, fmt.Errorf("%w: PRIV owner", ErrMetaFormat)
			}
			if len(f.Private) > MaxTextLen {
				return nil, fmt.Errorf("%w: PRIV data", ErrMetaLimit)
			}
			data = append(data, f.Owner...)
			data = append(data, 0)
			data = append(data, f.Private...)
		default:
			return nil, fmt.Errorf("%w: frame %q", ErrUnsupportedID3, f.ID)
		}
		size := encodeSyncSafe(len(data))
		body.WriteString(f.ID)
		body.Write(size[:])
		body.Write([]byte{0, 0}) // frame flags
		body.Write(data)
	}
	if body.Len() > MaxID3TagBytes {
		return nil, fmt.Errorf("%w: tag %d bytes", ErrMetaLimit, body.Len())
	}
	out := make([]byte, 0, 10+body.Len())
	out = append(out, 'I', 'D', '3', 4, 0, 0)
	size := encodeSyncSafe(body.Len())
	out = append(out, size[:]...)
	out = append(out, body.Bytes()...)
	return out, nil
}

// ParseID3 accepts the restricted profile: ID3v2.4, no unsynchronisation, no
// extended header, only known frames.
func ParseID3(b []byte) (ID3Tag, error) {
	var tag ID3Tag
	if len(b) < 10 {
		return tag, fmt.Errorf("%w: id3 header truncated", ErrMetaFormat)
	}
	if string(b[0:3]) != "ID3" {
		return tag, fmt.Errorf("%w: missing id3 magic", ErrMetaFormat)
	}
	if b[3] != 4 || b[4] != 0 {
		return tag, fmt.Errorf("%w: id3 version %d.%d", ErrUnsupportedID3, b[3], b[4])
	}
	if b[5]&id3FlagUnsynchronisation != 0 {
		return tag, fmt.Errorf("%w: unsynchronisation", ErrUnsupportedID3)
	}
	if b[5]&0x40 != 0 {
		return tag, fmt.Errorf("%w: extended header", ErrUnsupportedID3)
	}
	if b[5]&0x10 != 0 {
		return tag, fmt.Errorf("%w: footer", ErrUnsupportedID3)
	}
	if b[6]|b[7]|b[8]|b[9]&0x80 != 0 {
		return tag, fmt.Errorf("%w: non-syncsafe tag size", ErrMetaFormat)
	}
	size := syncSafe(binary.BigEndian.Uint32(b[6:10]))
	if size < 0 || 10+size > len(b) {
		return tag, fmt.Errorf("%w: tag size %d beyond buffer %d", ErrMetaFormat, size, len(b))
	}
	rest := b[10 : 10+size]
	for len(rest) > 0 {
		if len(rest) < 10 {
			return tag, fmt.Errorf("%w: frame header truncated", ErrMetaFormat)
		}
		id := string(rest[0:4])
		if !validFrameID(id) {
			return tag, fmt.Errorf("%w: frame id %q", ErrUnsupportedID3, id)
		}
		if rest[8] != 0 || rest[9] != 0 {
			return tag, fmt.Errorf("%w: frame %s flags", ErrUnsupportedID3, id)
		}
		if rest[4]|rest[5]|rest[6]|rest[7]&0x80 != 0 {
			return tag, fmt.Errorf("%w: non-syncsafe frame size", ErrMetaFormat)
		}
		fsize := syncSafe(binary.BigEndian.Uint32(rest[4:8]))
		rest = rest[10:]
		if fsize < 0 || fsize > len(rest) {
			return tag, fmt.Errorf("%w: frame %s size %d beyond %d", ErrMetaFormat, id, fsize, len(rest))
		}
		data := rest[:fsize]
		rest = rest[fsize:]
		if len(tag.Frames) >= MaxID3Frames {
			return tag, fmt.Errorf("%w: too many frames", ErrMetaLimit)
		}
		f, err := parseID3Frame(id, data)
		if err != nil {
			return tag, err
		}
		tag.Frames = append(tag.Frames, f)
	}
	if len(tag.Frames) == 0 {
		return tag, fmt.Errorf("%w: empty id3 tag", ErrMetaFormat)
	}
	return tag, nil
}

func validFrameID(id string) bool {
	if len(id) != 4 {
		return false
	}
	for i := 0; i < 4; i++ {
		c := id[i]
		if !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func parseID3Frame(id string, data []byte) (ID3Frame, error) {
	f := ID3Frame{ID: id}
	if len(data) == 0 {
		return f, fmt.Errorf("%w: frame %s empty", ErrMetaFormat, id)
	}
	body := data[1:]
	if id != ID3FramePRIV {
		f.Encoding = data[0]
		if f.Encoding != 3 {
			return f, fmt.Errorf("%w: frame %s encoding %d", ErrUnsupportedID3, id, f.Encoding)
		}
	}
	term := bytes.IndexByte(body, 0)
	switch id {
	case ID3FrameTIT2, ID3FrameTDRC:
		if term >= 0 {
			return f, fmt.Errorf("%w: frame %s padding", ErrMetaFormat, id)
		}
		if len(body) > MaxTextLen {
			return f, fmt.Errorf("%w: frame %s text", ErrMetaLimit, id)
		}
		f.Text = string(body)
		if id == ID3FrameTDRC {
			ms, err := parseISOTime(f.Text)
			if err != nil {
				return f, err
			}
			f.TimestampTicks = &ms
		}
	case ID3FrameTXXX:
		if term <= 0 || term > 128 {
			return f, fmt.Errorf("%w: TXXX owner", ErrMetaFormat)
		}
		f.Owner = string(body[:term])
		text := body[term+1:]
		if len(text) == 0 || len(text) > MaxTextLen {
			return f, fmt.Errorf("%w: TXXX value", ErrMetaLimit)
		}
		f.Text = string(text)
	case ID3FramePRIV:
		if term <= 0 || term > 128 {
			return f, fmt.Errorf("%w: PRIV owner", ErrMetaFormat)
		}
		f.Owner = string(body[:term])
		if len(body[term+1:]) > MaxTextLen {
			return f, fmt.Errorf("%w: PRIV data", ErrMetaLimit)
		}
		f.Private = append([]byte(nil), body[term+1:]...)
	default:
		return f, fmt.Errorf("%w: frame %q", ErrUnsupportedID3, id)
	}
	return f, nil
}

// parseISOTime parses the restricted TDRC form YYYY-MM-DDTHH:MM:SS.mmmZ and
// returns Unix milliseconds. It is deliberately stricter than time.Parse.
func parseISOTime(s string) (uint64, error) {
	var y, mo, d, h, mi, sec, ms int
	n, err := fmt.Sscanf(s, "%04d-%02d-%02dT%02d:%02d:%02d.%03dZ", &y, &mo, &d, &h, &mi, &sec, &ms)
	if err != nil || n != 7 || len(s) != 24 || s[23] != 'Z' {
		return 0, fmt.Errorf("%w: TDRC %q", ErrMetaFormat, s)
	}
	if y < 1970 || mo < 1 || mo > 12 || d < 1 || d > 31 || h > 23 || mi > 59 || sec > 60 {
		return 0, fmt.Errorf("%w: TDRC out of range %q", ErrMetaFormat, s)
	}
	days := daysFromCivil(y, mo, d)
	ms64 := int64(days)*86400000 + int64(h)*3600000 + int64(mi)*60000 + int64(sec)*1000 + int64(ms)
	if ms64 < 0 {
		return 0, fmt.Errorf("%w: TDRC before epoch", ErrMetaFormat)
	}
	return uint64(ms64), nil
}

func daysFromCivil(y, m, d int) int64 {
	if m <= 2 {
		y--
	}
	era := y / 400
	if y < 0 {
		era = (y - 399) / 400
	}
	yoe := y - era*400
	mp := (m + 9) % 12
	doy := (153*mp+2)/5 + d - 1
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return int64(era)*146097 + int64(doe) - 719468
}

// NewID3Record builds an ID3 metadata record at ms resolution.
func NewID3Record(trackID uint32, ticks uint64, tag ID3Tag) (Record, error) {
	payload, err := EncodeID3(tag)
	if err != nil {
		return Record{}, err
	}
	return NewRecord(KindID3, trackID, 1000, ticks, 0, "id3", payload)
}
