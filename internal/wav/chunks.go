package wav

import (
	"encoding/binary"
	"fmt"
	"io"
)

// parseHeader walks the RIFF chunk list. Chunks may appear in any order and
// unknown chunks are skipped; odd-sized chunks carry a pad byte.
func (r *Reader) parseHeader(size int64) error {
	var riff [12]byte
	if _, err := io.ReadFull(r.f, riff[:]); err != nil {
		return fmt.Errorf("wav: reading RIFF header: %w: %v", ErrTruncated, err)
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return fmt.Errorf("wav: not a RIFF/WAVE file")
	}
	riffEnd := int64(binary.LittleEndian.Uint32(riff[4:8])) + 8
	if riffEnd < 12 || riffEnd > size {
		return fmt.Errorf("wav: %w: RIFF size %d exceeds file size %d", ErrTruncated, riffEnd, size)
	}

	var haveFmt, haveData bool
	for pos := int64(12); pos+8 <= riffEnd; {
		var head [8]byte
		if _, err := r.f.ReadAt(head[:], pos); err != nil {
			return fmt.Errorf("wav: %w: reading chunk header: %v", ErrTruncated, err)
		}
		id := string(head[0:4])
		length := int64(binary.LittleEndian.Uint32(head[4:8]))
		body := pos + 8
		if body+length > size {
			return fmt.Errorf("wav: %w: chunk %q claims %d bytes at offset %d", ErrTruncated, id, length, pos)
		}
		switch id {
		case "fmt ":
			if haveFmt {
				return fmt.Errorf("wav: duplicate fmt chunk")
			}
			if err := r.parseFmt(body, length); err != nil {
				return err
			}
			haveFmt = true
		case "data":
			if haveData {
				return fmt.Errorf("wav: multiple data chunks are not supported")
			}
			r.header.DataOffset = body
			r.header.DataSize = length
			haveData = true
		default:
			// Unknown chunks (LIST, fact, cue, ...) are skipped.
		}
		pos = body + length
		if length%2 == 1 {
			pos++ // RIFF pad byte after odd-sized chunks.
		}
	}
	if !haveFmt {
		return fmt.Errorf("wav: missing fmt chunk")
	}
	if !haveData {
		return fmt.Errorf("wav: missing data chunk")
	}
	if r.header.DataSize%int64(r.header.BlockAlign) != 0 {
		return fmt.Errorf("wav: data size %d is not a multiple of block align %d",
			r.header.DataSize, r.header.BlockAlign)
	}
	return nil
}

// maxFmtBytes bounds the fmt chunk allocation. Valid PCM/float headers are
// 16 or 40 bytes; anything larger is a malformed or hostile file.
const maxFmtBytes = 4096

func (r *Reader) parseFmt(offset, length int64) error {
	if length < 16 {
		return fmt.Errorf("wav: fmt chunk is %d bytes, need at least 16", length)
	}
	if length > maxFmtBytes {
		return fmt.Errorf("wav: fmt chunk is %d bytes, limit is %d", length, maxFmtBytes)
	}
	raw := make([]byte, length)
	if _, err := r.f.ReadAt(raw, offset); err != nil {
		return fmt.Errorf("wav: reading fmt chunk: %w", err)
	}
	h := &r.header
	h.Format = binary.LittleEndian.Uint16(raw[0:2])
	h.Channels = int(binary.LittleEndian.Uint16(raw[2:4]))
	h.SampleRate = int(binary.LittleEndian.Uint32(raw[4:8]))
	h.BlockAlign = int(binary.LittleEndian.Uint16(raw[12:14]))
	h.Bits = int(binary.LittleEndian.Uint16(raw[14:16]))

	tag := h.Format
	if tag == FormatExtensible {
		if length < 40 {
			return fmt.Errorf("wav: extensible fmt chunk is %d bytes, need 40", length)
		}
		cbSize := int64(binary.LittleEndian.Uint16(raw[16:18]))
		if cbSize < 22 || 18+cbSize > length {
			return fmt.Errorf("wav: extensible fmt cbSize %d conflicts with chunk length %d", cbSize, length)
		}
		var suffix [12]byte
		copy(suffix[:], raw[28:40])
		if suffix != subFormatSuffix {
			return fmt.Errorf("wav: %w: unknown extensible sub-format", ErrFormat)
		}
		sub := binary.LittleEndian.Uint16(raw[24:26])
		if sub != FormatPCM && sub != FormatFloat {
			return fmt.Errorf("wav: %w: extensible sub-format tag %d", ErrFormat, sub)
		}
		tag = sub
	}

	switch {
	case tag == FormatPCM && (h.Bits == 16 || h.Bits == 24 || h.Bits == 32):
	case tag == FormatFloat && h.Bits == 32:
	default:
		return fmt.Errorf("wav: %w: tag %d, %d bits", ErrFormat, tag, h.Bits)
	}
	if h.Channels < 1 || h.Channels > 64 {
		return fmt.Errorf("wav: %w: %d channels", ErrFormat, h.Channels)
	}
	if h.SampleRate < 8000 || h.SampleRate > 384000 {
		return fmt.Errorf("wav: %w: sample rate %d", ErrFormat, h.SampleRate)
	}
	if want := h.Channels * ((h.Bits + 7) / 8); h.BlockAlign != want {
		return fmt.Errorf("wav: block align %d conflicts with %d channels at %d bits",
			h.BlockAlign, h.Channels, h.Bits)
	}
	h.Format = tag
	return nil
}
