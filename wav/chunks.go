// Package wav parses RIFF/WAVE container boundaries and validates PCM
// framing. Parsing never panics on malformed input: every defect is reported
// as a precisely anchored media.Fault instead of aborting the walk early
// without a result.
package wav

import (
	"encoding/binary"

	"mediad/media"
)

const (
	riffHeaderLen  = 12
	chunkHeaderLen = 8
	fmtMinPayload  = 16
)

// Chunk describes one RIFF chunk with its absolute offsets inside the raw
// input. DataEnd is the declared end and may exceed the buffer when the chunk
// is out of bounds; callers must cross-check against File.Limit.
type Chunk struct {
	ID          string
	HeaderStart int64
	Size        uint32
	DataStart   int64
	DataEnd     int64
}

// FmtChunk carries the decoded "fmt " payload plus its boundary offsets.
type FmtChunk struct {
	Chunk
	AudioFormat   uint16
	Channels      uint16
	SampleRate    uint32
	ByteRate      uint32
	BlockAlign    uint16
	BitsPerSample uint16
}

// File is the structural parse result. Limit is the safe walk boundary
// (min of declared RIFF end and actual buffer length).
type File struct {
	raw         []byte
	RIFFSize    uint32
	DeclaredEnd int64
	ActualLen   int64
	Limit       int64
	Chunks      []Chunk
	Fmt         *FmtChunk
	Data        *Chunk
	Faults      []media.Fault
}

// ParseChunks validates the RIFF/WAVE header and walks every chunk boundary.
// The returned File is always non-nil; faults describe everything that could
// not be trusted. Byte ranges in faults are half-open absolute intervals.
func ParseChunks(buf []byte) *File {
	f := &File{ActualLen: int64(len(buf)), raw: buf}

	if len(buf) < riffHeaderLen {
		end := int64(len(buf))
		f.Faults = append(f.Faults, media.NewByteFault(
			media.CatTruncatedHeader, 0, end,
			"RIFF/WAVE header needs %d bytes, got %d", riffHeaderLen, len(buf)))
		f.Limit = end
		return f
	}

	if string(buf[0:4]) != "RIFF" {
		f.Faults = append(f.Faults, media.NewByteFault(
			media.CatBadContainer, 0, 4, "not a RIFF container: got %q", string(buf[0:4])))
	}
	if string(buf[8:12]) != "WAVE" {
		f.Faults = append(f.Faults, media.NewByteFault(
			media.CatBadContainer, 8, 12, "not a WAVE stream: got %q", string(buf[8:12])))
	}
	f.RIFFSize = binary.LittleEndian.Uint32(buf[4:8])
	f.DeclaredEnd = int64(f.RIFFSize) + 8
	f.Limit = f.DeclaredEnd
	if f.DeclaredEnd > f.ActualLen {
		// The container itself ends before its declared size: a truncated
		// RIFF header/body. A chunk that merely overruns an internally
		// consistent RIFF is reported at the chunk level instead.
		f.Faults = append(f.Faults, media.NewByteFault(
			media.CatChunkOutOfBounds, f.ActualLen, f.DeclaredEnd,
			"RIFF declares %d bytes but buffer ends at %d", f.DeclaredEnd, f.ActualLen))
		f.Limit = f.ActualLen
	} else if f.DeclaredEnd < f.ActualLen {
		// Trailing bytes beyond the RIFF container are ignored, not trusted.
		f.Limit = f.DeclaredEnd
	}
	// A non-RIFF buffer cannot be walked safely.
	if len(f.Faults) > 0 && string(buf[0:4]) != "RIFF" {
		return f
	}

	pos := int64(riffHeaderLen)
	for pos < f.Limit {
		remain := f.Limit - pos
		if remain < chunkHeaderLen {
			f.Faults = append(f.Faults, media.NewByteFault(
				media.CatTruncatedHeader, pos, pos+remain,
				"chunk header needs %d bytes, %d remain", chunkHeaderLen, remain))
			break
		}
		id := string(buf[pos : pos+4])
		size := binary.LittleEndian.Uint32(buf[pos+4 : pos+8])
		c := Chunk{
			ID:          id,
			HeaderStart: pos,
			Size:        size,
			DataStart:   pos + chunkHeaderLen,
			DataEnd:     pos + chunkHeaderLen + int64(size),
		}
		f.Chunks = append(f.Chunks, c)
		if c.DataEnd > f.Limit {
			f.Faults = append(f.Faults, media.NewByteFault(
				media.CatChunkOutOfBounds, c.DataStart, c.DataEnd,
				"chunk %q declares %d payload bytes but walk limit is %d",
				id, size, f.Limit))
			// Keep the declared chunk (including fmt/data pointers) so the
			// caller can anchor later checks, then stop the walk.
			if id == "fmt " {
				f.Fmt = parseFmt(buf, c, f)
			}
			if id == "data" {
				dc := c
				f.Data = &dc
			}
			break
		}
		switch id {
		case "fmt ":
			f.Fmt = parseFmt(buf, c, f)
		case "data":
			dc := c
			f.Data = &dc
		}
		pos = c.DataEnd
		// RIFF pads odd-sized chunks with one byte; consume the pad only
		// when it is actually present inside the walk limit.
		if size%2 == 1 && pos < f.Limit {
			pos++
		}
	}

	if f.Fmt == nil {
		f.Faults = append(f.Faults, media.NewByteFault(
			media.CatBadContainer, 0, riffHeaderLen, "mandatory %q chunk is missing", "fmt "))
	}
	if f.Data == nil {
		f.Faults = append(f.Faults, media.NewByteFault(
			media.CatBadContainer, 0, riffHeaderLen, "mandatory %q chunk is missing", "data"))
	}
	return f
}

// parseFmt decodes the fmt payload, flagging a payload shorter than the
// mandatory 16 bytes without reading past the chunk boundary.
func parseFmt(buf []byte, c Chunk, f *File) *FmtChunk {
	fc := &FmtChunk{Chunk: c}
	if c.Size < fmtMinPayload {
		f.Faults = append(f.Faults, media.NewByteFault(
			media.CatTruncatedHeader, c.DataStart, c.DataEnd,
			"fmt payload needs %d bytes, got %d", fmtMinPayload, c.Size))
		return fc
	}
	p := c.DataStart
	fc.AudioFormat = binary.LittleEndian.Uint16(buf[p : p+2])
	fc.Channels = binary.LittleEndian.Uint16(buf[p+2 : p+4])
	fc.SampleRate = binary.LittleEndian.Uint32(buf[p+4 : p+8])
	fc.ByteRate = binary.LittleEndian.Uint32(buf[p+8 : p+12])
	fc.BlockAlign = binary.LittleEndian.Uint16(buf[p+12 : p+14])
	fc.BitsPerSample = binary.LittleEndian.Uint16(buf[p+14 : p+16])
	return fc
}
