package mp4

import (
	"bytes"
	"encoding/binary"
	"io"

	"mediatool/internal/timeline"
)

// TrackInfo describes one track declared in the init segment's moov box.
type TrackInfo struct {
	TrackID   uint32
	Timescale uint32
	MediaType string // "video", "audio" or ""
	CodecKey  string // e.g. "avc1", "mp4a", "enca"
}

// ParseInit extracts track metadata from a CMAF init segment (ftyp+moov).
// It performs a targeted, allocation-bounded walk rather than a full
// generic deserializer, so corrupted init data fails with locatable
// classified errors.
func ParseInit(r io.Reader) ([]TrackInfo, error) {
	var tracks []TrackInfo
	var moovBody []byte
	var moovOffset int64 = -1
	fatal := ScanBoxes(r, 0, MaxFragmentSize, func(box BoxHeader, payload io.Reader) error {
		if box.Type.String() != "moov" {
			return nil
		}
		body, err := io.ReadAll(payload)
		if err != nil {
			return err
		}
		moovBody = body
		moovOffset = int64(box.PayloadOffset)
		return nil
	})
	if fatal != nil {
		return nil, fatal
	}
	if moovBody == nil {
		return nil, timeline.ErrorAt(timeline.ClassFieldMissing, moovOffset,
			"init segment has no moov box")
	}
	err := ScanBoxes(bytes.NewReader(moovBody), moovOffset, MaxFragmentSize,
		func(box BoxHeader, payload io.Reader) error {
			if box.Type.String() != "trak" {
				return nil
			}
			body, rerr := io.ReadAll(payload)
			if rerr != nil {
				return rerr
			}
			info, perr := parseTrak(body, int64(box.PayloadOffset))
			if perr != nil {
				return perr
			}
			tracks = append(tracks, info)
			return nil
		})
	if err != nil {
		return nil, err
	}
	if len(tracks) == 0 {
		return nil, timeline.ErrorAt(timeline.ClassFieldMissing, moovOffset,
			"moov declares no tracks")
	}
	return tracks, nil
}

func parseTrak(body []byte, base int64) (TrackInfo, *timeline.MediaError) {
	info := TrackInfo{}
	err := ScanBoxes(bytes.NewReader(body), base, MaxFragmentSize,
		func(box BoxHeader, payload io.Reader) error {
			b, rerr := io.ReadAll(payload)
			if rerr != nil {
				return rerr
			}
			abs := int64(box.PayloadOffset)
			switch box.Type.String() {
			case "tkhd":
				if id, perr := parseTkhdTrackID(b, abs); perr != nil {
					return perr
				} else {
					info.TrackID = id
				}
			case "mdia":
				return parseMdia(b, abs, &info)
			}
			return nil
		})
	if err != nil {
		if me := timeline.AsMediaError(err); me != nil {
			return TrackInfo{}, me
		}
		return TrackInfo{}, timeline.ErrorAt(timeline.ClassFieldMissing, base, err.Error())
	}
	if info.TrackID == 0 {
		return TrackInfo{}, timeline.ErrorAt(timeline.ClassFieldMissing, base,
			"trak has no usable tkhd track id")
	}
	return info, nil
}

func parseMdia(body []byte, base int64, info *TrackInfo) error {
	return ScanBoxes(bytes.NewReader(body), base, MaxFragmentSize,
		func(box BoxHeader, payload io.Reader) error {
			b, err := io.ReadAll(payload)
			if err != nil {
				return err
			}
			abs := int64(box.PayloadOffset)
			switch box.Type.String() {
			case "mdhd":
				if ts, perr := parseMdhdTimescale(b, abs); perr != nil {
					return perr
				} else {
					info.Timescale = ts
				}
			case "hdlr":
				if mt, perr := parseHdlrType(b, abs); perr != nil {
					return perr
				} else {
					info.MediaType = mt
				}
			case "minf":
				if key, found := findCodecKey(b, abs); found {
					info.CodecKey = key
				}
			}
			return nil
		})
}

func parseTkhdTrackID(body []byte, abs int64) (uint32, *timeline.MediaError) {
	// FullBox(4); track id offset is 4 after version-dependent part:
	// version 0: creation(4)+modification(4)=8, then track_id(4).
	if len(body) < 1 {
		return 0, timeline.ErrorAt(timeline.ClassBoxTruncated, abs, "tkhd empty")
	}
	off := 4 + 8 + 4
	if body[0] == 1 {
		off = 4 + 16 + 4 // 64-bit creation/modification times
	}
	if len(body) < off {
		return 0, timeline.ErrorAt(timeline.ClassBoxTruncated, abs, "tkhd track id truncated")
	}
	return binary.BigEndian.Uint32(body[off-4:]), nil
}

func parseMdhdTimescale(body []byte, abs int64) (uint32, *timeline.MediaError) {
	if len(body) < 1 {
		return 0, timeline.ErrorAt(timeline.ClassBoxTruncated, abs, "mdhd empty")
	}
	off := 4 + 8 + 4
	if body[0] == 1 {
		off = 4 + 16 + 4
	}
	if len(body) < off {
		return 0, timeline.ErrorAt(timeline.ClassBoxTruncated, abs, "mdhd timescale truncated")
	}
	ts := binary.BigEndian.Uint32(body[off-4:])
	if ts == 0 {
		return 0, timeline.ErrorAt(timeline.ClassFieldMissing, abs, "mdhd timescale is zero")
	}
	return ts, nil
}

func parseHdlrType(body []byte, abs int64) (string, *timeline.MediaError) {
	// FullBox(4) + pre_defined(4) + handler_type(4)
	if len(body) < 12 {
		return "", timeline.ErrorAt(timeline.ClassBoxTruncated, abs, "hdlr handler type truncated")
	}
	handler := string(body[8:12])
	switch handler {
	case "vide":
		return "video", nil
	case "soun":
		return "audio", nil
	default:
		return handler, nil
	}
}

// findCodecKey walks minf/stbl/stsd for the first sample-entry type,
// which is the codec FourCC (avc1, hvc1, mp4a, enca, ...).
func findCodecKey(body []byte, base int64) (string, bool) {
	found := ""
	_ = ScanBoxes(bytes.NewReader(body), base, MaxFragmentSize,
		func(box BoxHeader, payload io.Reader) error {
			if box.Type.String() != "stbl" {
				return nil
			}
			stbl, err := io.ReadAll(payload)
			if err != nil {
				return err
			}
			return ScanBoxes(bytes.NewReader(stbl), int64(box.PayloadOffset), MaxFragmentSize,
				func(inner BoxHeader, p2 io.Reader) error {
					if inner.Type.String() != "stsd" || found != "" {
						return nil
					}
					sd, err := io.ReadAll(p2)
					if err != nil {
						return err
					}
					// FullBox(4) + entry_count(4); each entry starts with
					// a normal box header whose type is the codec FourCC.
					if len(sd) < 8 {
						return timeline.ErrorAt(timeline.ClassBoxTruncated,
							int64(inner.PayloadOffset), "stsd entries truncated")
					}
					if len(sd) >= 16 {
						found = string(sd[12:16])
					}
					return nil
				})
		})
	if found == "" {
		return "", false
	}
	return found, true
}
