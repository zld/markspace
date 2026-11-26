package metadata

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/abema/go-mp4"

	"replaypipe/internal/timebase"
)

// ID3 tag markers and the Apple transport-stream timestamp owner.
const (
	id3HeaderSize   = 10
	id3FooterSize   = 10
	tsPrivOwner     = "com.apple.streaming.transportStreamTimestamp"
	mpegTSClockRate = 90000
	maxID3Frames    = 4096
)

// ParseID3 decodes an ID3v2 tag and returns one event per timed frame.
// Frames without their own timestamp are anchored at opts.AnchorNS, which is
// the presentation time of the sample that carried the tag. Malformed or
// truncated tags are reported with the byte offset inside the tag.
func ParseID3(tag []byte, opts ParseOptions) ([]Event, *timebase.Error) {
	if len(tag) < id3HeaderSize {
		return nil, timebase.Newf(timebase.CodeBoxTruncated, opts.TrackID, opts.BaseOffset,
			"id3 tag has %d bytes, need at least %d", len(tag), id3HeaderSize)
	}
	if !bytes.Equal(tag[:3], []byte("ID3")) {
		return nil, timebase.Newf(timebase.CodeCorruptField, opts.TrackID, opts.BaseOffset,
			"id3 tag missing ID3 signature")
	}
	version := tag[3]
	if version < 2 || version > 4 {
		return nil, timebase.Newf(timebase.CodeUnsupportedFormat, opts.TrackID, opts.BaseOffset,
			"id3 version 2.%d is not supported", version)
	}
	flags := tag[5]
	if tag[6]|tag[7]|tag[8]|tag[9] != 0 || tag[6]&0x80 != 0 {
		return nil, timebase.Newf(timebase.CodeCorruptField, opts.TrackID, opts.BaseOffset,
			"id3 syncsafe size has high bits set")
	}
	size := syncsafe(tag[6:10])
	end := id3HeaderSize + size
	if flags&0x10 != 0 {
		end += id3FooterSize
	}
	if end > len(tag) {
		return nil, timebase.Newf(timebase.CodeBoxTruncated, opts.TrackID, opts.BaseOffset+int64(len(tag)),
			"id3 tag claims %d bytes but only %d present", end, len(tag))
	}
	body := tag[id3HeaderSize : id3HeaderSize+size]
	if flags&0x40 != 0 { // extended header
		if len(body) < 4 {
			return nil, timebase.Newf(timebase.CodeBoxTruncated, opts.TrackID, opts.BaseOffset+id3HeaderSize,
				"id3 extended header truncated")
		}
		var extLen int
		if version == 4 {
			extLen = syncsafe(body[:4])
		} else {
			extLen = int(binary.BigEndian.Uint32(body[:4])) + 4
		}
		if extLen < 4 || extLen > len(body) {
			return nil, timebase.Newf(timebase.CodeCorruptField, opts.TrackID, opts.BaseOffset+id3HeaderSize,
				"id3 extended header length %d invalid", extLen)
		}
		body = body[extLen:]
	}

	var tsTicks int64 = -1
	var title, desc, textVal string
	frameIDLen, frameSizeLen := 4, 4
	if version == 2 {
		frameIDLen, frameSizeLen = 3, 3
	}
	off := 0
	for i := 0; i < maxID3Frames && off+frameIDLen+frameSizeLen <= len(body); i++ {
		id := string(body[off : off+frameIDLen])
		if strings.Trim(id, "\x00") == "" {
			break
		}
		szOff := off + frameIDLen
		var fsz int
		if version == 2 {
			fsz = int(body[szOff])<<16 | int(body[szOff+1])<<8 | int(body[szOff+2])
		} else if version == 4 {
			if body[szOff]&0x80 != 0 || body[szOff+1]&0x80 != 0 ||
				body[szOff+2]&0x80 != 0 || body[szOff+3]&0x80 != 0 {
				return nil, timebase.Newf(timebase.CodeCorruptField, opts.TrackID, opts.BaseOffset+id3HeaderSize+int64(szOff),
					"id3 frame %q size has syncsafe high bits", id)
			}
			fsz = syncsafe(body[szOff : szOff+4])
		} else {
			fsz = int(binary.BigEndian.Uint32(body[szOff : szOff+4]))
		}
		dataOff := szOff + frameSizeLen
		if version > 2 {
			dataOff += 2 // frame flags
		}
		if fsz < 0 || dataOff+fsz > len(body) {
			return nil, timebase.Newf(timebase.CodeBoxTruncated, opts.TrackID,
				opts.BaseOffset+id3HeaderSize+int64(dataOff),
				"id3 frame %q claims %d bytes, %d remain", id, fsz, len(body)-dataOff)
		}
		payload := body[dataOff : dataOff+fsz]
		switch id {
		case "PRIV":
			owner, rest := splitEncoded(payload)
			if owner == tsPrivOwner && len(rest) >= 8 {
				tsTicks = int64(binary.BigEndian.Uint64(rest[len(rest)-8:]) & 0x1FFFFFFFF)
			}
		case "TIT2", "TT2":
			title = decodeTextFrame(payload)
		case "TXXX", "TXX":
			desc, textVal = splitTextFrame(payload)
		}
		off = dataOff + fsz
	}

	startNS := opts.AnchorNS
	tbDesc := "anchor"
	if tsTicks >= 0 {
		tsBase, err := timebase.FromTimescale(mpegTSClockRate)
		if err != nil {
			return nil, timebase.Fail(timebase.CodeInternal, opts.TrackID, opts.BaseOffset).Wrap(err, "id3 clock")
		}
		ns, e := tsBase.ToNanos(tsTicks, opts.TrackID, opts.BaseOffset)
		if e != nil {
			return nil, e
		}
		startNS = ns
		tbDesc = tsBase.String() + " (id3 priv)"
	}
	idBody := firstOf(title, textVal, desc, "id3")
	ev := Event{
		ID:           fmt.Sprintf("id3-%s-%d", slug(idBody), startNS),
		Kind:         KindID3,
		TrackID:      opts.TrackID,
		Source:       "id3",
		StartNS:      startNS,
		EndNS:        startNS,
		Timebase:     tbDesc,
		Text:         strings.TrimSpace(firstNonEmpty(title, textVal)),
		SourceOffset: opts.BaseOffset,
	}
	if e := ev.Validate(opts); e != nil {
		return nil, e
	}
	return []Event{ev}, nil
}

// ParseEmsg decodes a complete emsg box payload (without the 8-byte header).
// Version 0 presentation_time_delta is relative to anchorNS; version 1 carries
// an absolute presentation time. Durations use the emsg timescale.
func ParseEmsg(payload []byte, opts ParseOptions) ([]Event, *timebase.Error) {
	if len(payload) < 4 {
		return nil, timebase.Newf(timebase.CodeBoxTruncated, opts.TrackID, opts.BaseOffset,
			"emsg payload has %d bytes", len(payload))
	}
	version := payload[0]
	box, n, err := mp4.UnmarshalAny(bytes.NewReader(payload), mp4.BoxTypeEmsg(),
		uint64(len(payload)), mp4.Context{})
	if err != nil {
		code := timebase.CodeCorruptField
		if strings.Contains(err.Error(), "overrun") || strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "bits") {
			code = timebase.CodeBoxTruncated
		}
		return nil, timebase.Fail(code, opts.TrackID, opts.BaseOffset).Wrap(err, "decode emsg v%d", version)
	}
	if n > uint64(len(payload)) {
		return nil, timebase.Newf(timebase.CodeBoxTruncated, opts.TrackID, opts.BaseOffset,
			"emsg consumed %d of %d bytes", n, len(payload))
	}
	emsg, ok := box.(*mp4.Emsg)
	if !ok {
		return nil, timebase.Newf(timebase.CodeCorruptField, opts.TrackID, opts.BaseOffset,
			"eme box decoded as %T", box)
	}
	if emsg.Timescale == 0 {
		return nil, timebase.Newf(timebase.CodeCorruptField, opts.TrackID, opts.BaseOffset,
			"emsg %q has zero timescale", emsg.SchemeIdUri)
	}
	base, err := timebase.FromTimescale(emsg.Timescale)
	if err != nil {
		return nil, timebase.Fail(timebase.CodeCorruptField, opts.TrackID, opts.BaseOffset).Wrap(err, "emsg timescale")
	}
	var startNS int64
	if emsg.GetVersion() == 1 {
		ns, e := base.ToNanos(clampU64(emsg.PresentationTime), opts.TrackID, opts.BaseOffset)
		if e != nil {
			return nil, e
		}
		startNS = ns
	} else {
		delta, e := base.ToNanos(int64(emsg.PresentationTimeDelta), opts.TrackID, opts.BaseOffset)
		if e != nil {
			return nil, e
		}
		startNS = opts.AnchorNS + delta
	}
	durNS := int64(0)
	if emsg.EventDuration > 0 {
		d, e := base.NanoDuration(int64(emsg.EventDuration), opts.TrackID, opts.BaseOffset)
		if e != nil {
			return nil, e
		}
		durNS = d
	}
	text := strings.TrimRight(string(emsg.MessageData), "\x00")
	ev := Event{
		ID:           fmt.Sprintf("emsg-%d-%s", emsg.Id, slug(firstNonEmpty(emsg.Value, emsg.SchemeIdUri))),
		Kind:         KindEmsg,
		TrackID:      opts.TrackID,
		Source:       "emsg:" + emsg.SchemeIdUri,
		StartNS:      startNS,
		EndNS:        startNS + durNS,
		Timebase:     base.String() + " (emsg)",
		Text:         text,
		SourceOffset: opts.BaseOffset,
	}
	if e := ev.Validate(opts); e != nil {
		return nil, e
	}
	return []Event{ev}, nil
}

func clampU64(v uint64) int64 {
	if v > uint64(1<<63-1) {
		return 1<<63 - 1
	}
	return int64(v)
}

func syncsafe(b []byte) int {
	return int(b[0]&0x7f)<<21 | int(b[1]&0x7f)<<14 | int(b[2]&0x7f)<<7 | int(b[3]&0x7f)
}

// splitEncoded splits an ID3 frame whose first byte selects ISO-8859-1 (0),
// UTF-16 (1/2) or UTF-8 (3) and returns the NUL-separated strings.
func splitEncoded(payload []byte) (string, []byte) {
	if len(payload) == 0 {
		return "", nil
	}
	enc := payload[0]
	body := payload[1:]
	switch enc {
	case 0, 3:
		i := bytes.IndexByte(body, 0)
		if i < 0 {
			return string(body), nil
		}
		return string(body[:i]), body[i+1:]
	case 1, 2:
		for i := 0; i+1 < len(body); i += 2 {
			if body[i] == 0 && body[i+1] == 0 {
				return decodeUTF16(body[:i], enc == 2), body[i+2:]
			}
		}
		return decodeUTF16(body, enc == 2), nil
	default:
		return string(body), nil
	}
}

func decodeTextFrame(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	s, _ := splitEncoded(payload)
	return strings.TrimSpace(strings.Trim(s, "\x00"))
}

func splitTextFrame(payload []byte) (string, string) {
	if len(payload) == 0 {
		return "", ""
	}
	desc, rest := splitEncoded(payload)
	return strings.TrimSpace(desc), strings.TrimSpace(strings.Trim(decodeBytes(rest, payload[0]), "\x00"))
}

func decodeBytes(b []byte, enc byte) string {
	if enc == 1 || enc == 2 {
		return decodeUTF16(b, enc == 2)
	}
	return string(b)
}

func decodeUTF16(b []byte, bigEndian bool) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if bigEndian {
			u = append(u, binary.BigEndian.Uint16(b[i:]))
		} else {
			u = append(u, binary.LittleEndian.Uint16(b[i:]))
		}
	}
	return string(utf16.Decode(u))
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
