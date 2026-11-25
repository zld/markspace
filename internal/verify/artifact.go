package verify

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// WriteEvent is the writer's input. It mirrors Record without the in-file
// offset, which only the reader knows.
type WriteEvent struct {
	TimeNs  int64
	Segment int
	Track   string
	Source  string
	Text    string
}

// WriteParams are the echoed plan parameters.
type WriteParams struct {
	SegmentMS   int
	PrecisionUS int
}

// Encode builds artifact bytes in layout version 1. The output depends only
// on the inputs, so re-running the same plan over the same events is
// byte-identical; that is what makes the manifest's artifact digest stable
// across interrupts and re-deliveries.
//
// Track and source are single bytes in the format; their values are limited
// to the carriers this kernel produces, and anything else is a programming
// error surfaced as an explicit failure rather than a truncated string.
func Encode(events []WriteEvent, segs []Segment, params WriteParams) ([]byte, error) {
	if params.SegmentMS <= 0 || params.PrecisionUS <= 0 {
		return nil, fmt.Errorf("invalid plan parameters: segment=%dms precision=%dus",
			params.SegmentMS, params.PrecisionUS)
	}
	body := make([]byte, 0, len(events)*EventFixedSize+64)
	textBytes := 0
	for i, ev := range events {
		track, err := codeByte(trackCodes, ev.Track)
		if err != nil {
			return nil, fmt.Errorf("event %d: %w", i+1, err)
		}
		source, err := codeByte(sourceCodes, ev.Source)
		if err != nil {
			return nil, fmt.Errorf("event %d: %w", i+1, err)
		}
		if ev.Segment < 0 || ev.Segment >= len(segs) {
			return nil, fmt.Errorf("event %d names segment %d of %d", i+1, ev.Segment, len(segs))
		}
		if len(ev.Text) > 0xffff {
			return nil, fmt.Errorf("event %d text is %d bytes, the format allows 65535",
				i+1, len(ev.Text))
		}
		rec := make([]byte, EventFixedSize)
		binary.BigEndian.PutUint64(rec[0:8], uint64(ev.TimeNs))
		binary.BigEndian.PutUint32(rec[8:12], uint32(ev.Segment))
		binary.BigEndian.PutUint16(rec[12:14], uint16(len(ev.Text)))
		// Bytes 14-15 are flags (zero); track and source live at 16 and 17.
		rec[16] = track
		rec[17] = source
		body = append(body, rec...)
		body = append(body, ev.Text...)
		textBytes += len(ev.Text)
	}
	sum := sha256.Sum256(body)

	header := make([]byte, HeaderSize)
	copy(header[0:4], Magic)
	binary.BigEndian.PutUint16(header[4:6], LayoutVersion)
	binary.BigEndian.PutUint16(header[6:8], 0)
	binary.BigEndian.PutUint32(header[8:12], uint32(params.SegmentMS))
	binary.BigEndian.PutUint32(header[12:16], uint32(params.PrecisionUS))
	binary.BigEndian.PutUint32(header[16:20], uint32(len(events)))
	binary.BigEndian.PutUint32(header[20:24], uint32(len(segs)))
	binary.BigEndian.PutUint32(header[24:28], uint32(textBytes))
	binary.BigEndian.PutUint32(header[28:32], uint32(len(body)))
	copy(header[DigestOffset:DigestOffset+32], sum[:])

	segTable := make([]byte, 0, len(segs)*SegEntrySize)
	for _, s := range segs {
		entry := make([]byte, SegEntrySize)
		binary.BigEndian.PutUint64(entry[0:8], uint64(s.StartNs))
		binary.BigEndian.PutUint32(entry[8:12], uint32(s.EventCount))
		segTable = append(segTable, entry...)
	}
	out := make([]byte, 0, HeaderSize+len(segTable)+len(body))
	out = append(out, header...)
	out = append(out, segTable...)
	out = append(out, body...)
	return out, nil
}

var trackCodes = map[string]byte{
	"meta": 1, "subtitle": 2, "chapter": 3, "id3": 4, "video": 5, "audio": 6,
}

var sourceCodes = map[string]byte{
	"subtitle": 1, "chapter": 2, "id3": 3, "emsg": 4, "sei": 5,
}

func codeByte(m map[string]byte, v string) (byte, error) {
	if b, ok := m[v]; ok {
		return b, nil
	}
	return 0, fmt.Errorf("unsupported identifier %q", v)
}

// Decode identifier bytes back to names, for reader convenience.
func trackName(b byte) string {
	for k, v := range trackCodes {
		if v == b {
			return k
		}
	}
	return "?"
}

func sourceName(b byte) string {
	for k, v := range sourceCodes {
		if v == b {
			return k
		}
	}
	return "?"
}
