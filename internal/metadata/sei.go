package metadata

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"

	"replaypipe/internal/timebase"
)

// SEI user_data_unregistered UUID identifying replaypipe events. The payload
// layout after the UUID is: 8-byte big-endian nanosecond timestamp followed by
// UTF-8 text. SEI without this UUID is ignored; SEI with a malformed payload is
// reported, never dropped silently.
var seiUUID = [16]byte{
	0x52, 0x50, 0x49, 0x50, 0x45, 0x2d, 0x53, 0x45,
	0x49, 0x2d, 0x45, 0x56, 0x4e, 0x54, 0x01, 0x00,
}

var startCode4 = []byte{0x00, 0x00, 0x00, 0x01}
var startCode3 = []byte{0x00, 0x00, 0x01}

// NALUnit is one Annex B NAL unit with its absolute byte offset.
type NALUnit struct {
	Type   byte
	Data   []byte // includes the NAL header byte
	Offset int64
}

// SplitAnnexB splits an Annex B byte stream into NAL units, tolerating both
// 3- and 4-byte start codes. Truncated trailing data is returned with a
// BOX_TRUNCATED error so callers can stop the affected fragment.
func SplitAnnexB(data []byte, baseOffset int64) ([]NALUnit, *timebase.Error) {
	var nals []NALUnit
	i := 0
	for {
		idx := bytes.Index(data[i:], startCode3)
		if idx < 0 {
			if len(bytes.Trim(data[i:], "\x00")) > 0 && len(nals) > 0 {
				return nals, timebase.Newf(timebase.CodeBoxTruncated, 0, baseOffset+int64(i),
					"trailing %d bytes after last NAL start code", len(data)-i)
			}
			break
		}
		start := i + idx
		header := start + 3
		if start+4 <= len(data) && data[start] == 0 && data[start+1] == 0 && data[start+2] == 0 && data[start+3] == 1 {
			header = start + 4
		}
		if header >= len(data) {
			return nals, timebase.Newf(timebase.CodeBoxTruncated, 0, baseOffset+int64(start),
				"start code at end of buffer")
		}
		next := bytes.Index(data[header:], startCode3)
		end := len(data)
		if next >= 0 {
			end = header + next
		}
		nal := data[header:end]
		// Trim zero padding before the next start code.
		trimmed := len(nal)
		for trimmed > 1 && nal[trimmed-1] == 0 {
			trimmed--
		}
		nal = nal[:trimmed]
		nals = append(nals, NALUnit{Type: nal[0] & 0x1f, Data: nal, Offset: baseOffset + int64(header)})
		if next < 0 {
			break
		}
		i = end
	}
	return nals, nil
}

// ParseSEI extracts replaypipe timed events from H.264 SEI NAL units in an
// Annex B sample. Events are timed by the embedded nanosecond timestamp so
// that muxing cannot shift them; a message lacking a timestamp falls back to
// opts.AnchorNS.
func ParseSEI(sample []byte, opts ParseOptions) ([]Event, *timebase.Error) {
	nals, err := SplitAnnexB(sample, opts.BaseOffset)
	if err != nil {
		return nil, err
	}
	var events []Event
	for _, nal := range nals {
		if nal.Type != 6 || len(nal.Data) < 2 {
			continue
		}
		payload := unescapeRBSP(nal.Data[1:])
		evs, e := parseSEIPayload(payload, nal.Offset, opts)
		if e != nil {
			return nil, e
		}
		events = append(events, evs...)
	}
	if e := ValidateOrder(events, opts); e != nil {
		return nil, e
	}
	return events, nil
}

func parseSEIPayload(payload []byte, offset int64, opts ParseOptions) ([]Event, *timebase.Error) {
	var events []Event
	i := 0
	for i < len(payload) {
		if payload[i] == 0x80 && allZero(payload[i+1:]) {
			break // rbsp_trailing_bits
		}
		ptype, n, ok := readSEIValue(payload[i:])
		if !ok {
			return nil, timebase.Newf(timebase.CodeCorruptField, opts.TrackID, offset,
				"sei payload type overflow at byte %d", i)
		}
		i += n
		if i >= len(payload) {
			return nil, timebase.Newf(timebase.CodeBoxTruncated, opts.TrackID, offset+int64(i),
				"sei payload size missing")
		}
		psize, n, ok := readSEIValue(payload[i:])
		if !ok {
			return nil, timebase.Newf(timebase.CodeCorruptField, opts.TrackID, offset+int64(i),
				"sei payload size overflow")
		}
		i += n
		if i+psize > len(payload) {
			return nil, timebase.Newf(timebase.CodeBoxTruncated, opts.TrackID, offset+int64(i),
				"sei payload type %d claims %d bytes, %d remain", ptype, psize, len(payload)-i)
		}
		msg := payload[i : i+psize]
		i += psize
		if ptype != 5 || len(msg) < 16 {
			continue
		}
		var uuid [16]byte
		copy(uuid[:], msg[:16])
		if uuid != seiUUID {
			continue
		}
		body := msg[16:]
		startNS := opts.AnchorNS
		text := ""
		if len(body) >= 8 {
			startNS = int64(binary.BigEndian.Uint64(body[:8]))
			text = string(body[8:])
		} else {
			text = string(body)
		}
		text = strings.TrimRight(text, "\x00")
		ev := Event{
			ID:           fmt.Sprintf("sei-%d", startNS),
			Kind:         KindSEI,
			TrackID:      opts.TrackID,
			Source:       "sei",
			StartNS:      startNS,
			EndNS:        startNS,
			Timebase:     "ns (sei)",
			Text:         text,
			SourceOffset: offset + int64(i-psize),
		}
		if e := ev.Validate(opts); e != nil {
			return nil, e
		}
		events = append(events, ev)
	}
	return events, nil
}

func readSEIValue(b []byte) (int, int, bool) {
	v := 0
	for i := 0; i < len(b); i++ {
		v += int(b[i])
		if v > 1<<24 {
			return 0, 0, false
		}
		if b[i] != 0xff {
			return v, i + 1, true
		}
	}
	return 0, 0, false
}

func unescapeRBSP(b []byte) []byte {
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 0x03 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// EncodeSEI builds a single-NAL Annex B SEI carrying a replaypipe event. It
// exists for fixtures, tests and round-trip verification.
func EncodeSEI(startNS int64, text string) []byte {
	body := make([]byte, 0, 24+len(text))
	body = append(body, seiUUID[:]...)
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(startNS))
	body = append(body, ts[:]...)
	body = append(body, []byte(text)...)
	msg := []byte{5}
	msg = appendSEIValue(msg, len(body))
	msg = append(msg, body...)
	msg = append(msg, 0x80)
	rb := escapeRBSP(msg)
	nal := append([]byte{0x06}, rb...)
	out := append([]byte{}, startCode4...)
	return append(out, nal...)
}

func appendSEIValue(dst []byte, v int) []byte {
	for v >= 255 {
		dst = append(dst, 0xff)
		v -= 255
	}
	return append(dst, byte(v))
}

func escapeRBSP(b []byte) []byte {
	out := make([]byte, 0, len(b)+4)
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c <= 0x03 {
			out = append(out, 0x03)
			zeros = 0
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}
