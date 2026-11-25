package cmaf

import (
	"encoding/binary"
	"fmt"

	mp4 "github.com/abema/go-mp4"

	"replay/internal/model"
)

// ParseInit parses an init segment (ftyp+moov), extracting per-track
// timescale, audio layout/sample rate and video color metadata.
func (p *Parser) ParseInit(path string) (*Init, error) {
	f, err := openFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	init := &Init{Path: path}
	var cur *TrackInfo
	err = walk(f, 0, fileSize(f), 0, func(bi *mp4.BoxInfo, _ int) error {
		switch bi.Type {
		case mp4.BoxTypeTrak():
			init.Tracks = append(init.Tracks, TrackInfo{})
			cur = &init.Tracks[len(init.Tracks)-1]
		case mp4.BoxTypeTkhd():
			var b mp4.Tkhd
			if err := unmarshalBox(f, bi, &b); err != nil {
				return err
			}
			if cur != nil {
				cur.TrackID = b.TrackID
			}
		case mp4.BoxTypeMdhd():
			var b mp4.Mdhd
			if err := unmarshalBox(f, bi, &b); err != nil {
				return err
			}
			if cur != nil {
				cur.Timescale = b.Timescale
			}
		case mp4.BoxTypeHdlr():
			var b mp4.Hdlr
			if err := unmarshalBox(f, bi, &b); err != nil {
				return err
			}
			if cur != nil {
				switch string(b.HandlerType[:]) {
				case "vide":
					cur.Type = TrackVideo
				case "soun":
					cur.Type = TrackAudio
				}
			}
		case mp4.BoxTypeStsd():
			payload, err := readPayload(f, bi)
			if err != nil {
				return err
			}
			if cur != nil {
				return parseStsd(payload, cur)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(init.Tracks) == 0 {
		return nil, fmt.Errorf("cmaf: %s: no tracks in init segment", path)
	}
	for _, t := range init.Tracks {
		if t.Timescale == 0 {
			return nil, fmt.Errorf("cmaf: %s: track %d has zero timescale", path, t.TrackID)
		}
	}
	return init, nil
}

// parseStsd reads sample entries directly: go-mp4 does not model every
// codec-specific entry, and the fields we need sit at fixed offsets.
func parseStsd(payload []byte, cur *TrackInfo) error {
	if len(payload) < 8 {
		return fmt.Errorf("cmaf: stsd payload too short (%d bytes)", len(payload))
	}
	n := binary.BigEndian.Uint32(payload[4:8])
	off := 8
	for i := uint32(0); i < n; i++ {
		if off+8 > len(payload) {
			return fmt.Errorf("cmaf: stsd entry %d truncated", i)
		}
		size := int(binary.BigEndian.Uint32(payload[off:]))
		typ := string(payload[off+4 : off+8])
		if size < 8 || off+size > len(payload) {
			return fmt.Errorf("cmaf: stsd entry %d (%s) declares %d bytes, %d remain",
				i, typ, size, len(payload)-off)
		}
		entry := payload[off+8 : off+size]
		switch typ {
		case "avc1", "avc3", "hvc1", "hev1", "av01":
			if len(entry) < 78 {
				return fmt.Errorf("cmaf: %s entry too short (%d bytes)", typ, len(entry))
			}
			cur.Width = uint32(binary.BigEndian.Uint16(entry[24:]))
			cur.Height = uint32(binary.BigEndian.Uint16(entry[26:]))
			// VisualSampleEntry header is 78 bytes; children (avcC, colr)
			// follow it.
			scanVisualChildren(entry[78:], cur)
		case "mp4a":
			if len(entry) < 28 {
				return fmt.Errorf("cmaf: mp4a entry too short (%d bytes)", len(entry))
			}
			cur.Audio = &model.AudioInfo{
				Channels:   int(binary.BigEndian.Uint16(entry[16:])),
				SampleRate: binary.BigEndian.Uint32(entry[24:]) >> 16, // 16.16 fixed point
			}
		}
		off += size
	}
	return nil
}

// scanVisualChildren walks boxes nested inside a visual sample entry,
// looking for colr (nclx) color information.
func scanVisualChildren(b []byte, cur *TrackInfo) {
	off := 0
	for off+8 <= len(b) {
		size := int(binary.BigEndian.Uint32(b[off:]))
		if size < 8 || off+size > len(b) {
			return
		}
		if string(b[off+4:off+8]) == "colr" && size >= 19 && string(b[off+8:off+12]) == "nclx" {
			cur.Color = &model.ColorInfo{
				Primaries: colorName(int(binary.BigEndian.Uint16(b[off+12:]))),
				Transfer:  colorName(int(binary.BigEndian.Uint16(b[off+14:]))),
				Matrix:    colorName(int(binary.BigEndian.Uint16(b[off+16:]))),
			}
		}
		off += size
	}
}

// colorName maps ISO/IEC 23001-8 code points to names used in policies.
func colorName(code int) string {
	switch code {
	case 1:
		return "bt709"
	case 9, 14, 15:
		return "bt2020"
	case 2:
		return "unspecified"
	default:
		return fmt.Sprintf("code-%d", code)
	}
}
