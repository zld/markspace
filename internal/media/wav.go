// Package media parses PCM/WAV multi-channel sample blocks and validates
// block boundaries and sample integrity.
package media

import (
	"encoding/binary"

	"mediasync/internal/clock"
)

// MaxInputSize bounds a single input to 2 GiB.
const MaxInputSize int64 = 2 << 30

// MaxIndexRecords bounds the random-access index to 5 million records.
const MaxIndexRecords int64 = 5_000_000

// WAVHeader holds the validated PCM format description.
type WAVHeader struct {
	Channels      int
	SampleRate    int
	BitsPerSample int
	BlockAlign    int // bytes per sample frame
	ByteRate      int64
	DataOffset    int64 // absolute offset where PCM payload starts
	DataSize      int64 // size declared by the data chunk
}

// WAVFile is a parsed PCM WAV input. When the data chunk is truncated the
// parser still returns a usable file with Truncated=true and the structured
// error describing offset and missing byte count; callers decide whether
// the surviving prefix is acceptable.
type WAVFile struct {
	Header      WAVHeader
	PCM         []byte // bytes actually available (may be < DataSize)
	Truncated   bool
	Missing     int64
	TruncOffset int64
}

// FrameCount returns the number of complete sample frames in PCM.
func (w *WAVFile) FrameCount() int64 { return int64(len(w.PCM)) / int64(w.Header.BlockAlign) }

// TailBytes returns PCM bytes that do not form a complete frame.
func (w *WAVFile) TailBytes() int { return len(w.PCM) % w.Header.BlockAlign }

// ParseWAV parses a complete RIFF/WAVE byte slice. Only uncompressed PCM
// (format tag 1), 16/24-bit samples and 1..8 channels are accepted. Every
// short read produces a structured clock.TruncationError carrying the byte
// offset and the number of missing bytes; nothing is zero-padded.
func ParseWAV(b []byte) (*WAVFile, error) {
	if int64(len(b)) > MaxInputSize {
		return nil, clock.ErrorAt(clock.ErrLimitExceeded, int64(len(b)),
			"input %d bytes exceeds 2 GiB limit", len(b))
	}
	if len(b) < 12 {
		return nil, clock.TruncationError(int64(len(b)), int64(12-len(b)), "RIFF header")
	}
	if string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, clock.ErrorAt(clock.ErrInvalidArgument, 0, "not a RIFF/WAVE container")
	}
	riffSize := int64(binary.LittleEndian.Uint32(b[4:8]))
	if riffSize+8 > MaxInputSize {
		return nil, clock.ErrorAt(clock.ErrOverflow, 4, "declared RIFF size %d overflows limit", riffSize)
	}

	w := &WAVFile{}
	off := int64(12)
	var sawFmt, sawData bool
	for off+8 <= int64(len(b)) {
		id := string(b[off : off+4])
		size := int64(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		payload := off + 8
		if size < 0 || payload+size > MaxInputSize {
			return nil, clock.ErrorAt(clock.ErrOverflow, off+4, "chunk size %d overflows", size)
		}
		if payload+size > int64(len(b)) {
			// Chunk header present but payload incomplete: report and, for
			// the data chunk, keep the surviving prefix.
			missing := payload + size - int64(len(b))
			if id == "data" {
				w.Header.DataOffset, w.Header.DataSize = payload, size
				w.PCM = b[payload:]
				w.Truncated, w.Missing, w.TruncOffset = true, missing, int64(len(b))
				sawData = true
				break
			}
			return nil, clock.TruncationError(int64(len(b)), missing, id+" chunk")
		}

		switch id {
		case "fmt ":
			if err := parseFmt(b[payload:payload+size], &w.Header); err != nil {
				return nil, err
			}
			sawFmt = true
		case "data":
			w.Header.DataOffset, w.Header.DataSize = payload, size
			w.PCM = b[payload : payload+size]
			sawData = true
		}
		off = payload + size + size%2 // chunks are word-aligned
	}

	if !sawFmt {
		return nil, clock.ErrorAt(clock.ErrInvalidArgument, off, "missing fmt chunk")
	}
	if !sawData {
		return nil, clock.ErrorAt(clock.ErrTruncated, off, "missing data chunk")
	}
	if w.Truncated {
		return w, clock.TruncationError(w.TruncOffset, w.Missing, "PCM data chunk")
	}
	if rem := w.TailBytes(); rem != 0 {
		return w, clock.TruncationError(int64(len(w.PCM))-int64(rem), int64(rem),
			"final sample frame")
	}
	return w, nil
}

func parseFmt(p []byte, h *WAVHeader) error {
	if len(p) < 16 {
		return clock.TruncationError(int64(len(p)), int64(16-len(p)), "fmt payload")
	}
	if tag := binary.LittleEndian.Uint16(p[0:2]); tag != 1 {
		return clock.ErrorAt(clock.ErrInvalidArgument, 0, "unsupported format tag %d (PCM only)", tag)
	}
	ch := int(binary.LittleEndian.Uint16(p[2:4]))
	rate := int(binary.LittleEndian.Uint32(p[4:8]))
	byteRate := int64(binary.LittleEndian.Uint32(p[8:12]))
	blockAlign := int(binary.LittleEndian.Uint16(p[12:14]))
	bits := int(binary.LittleEndian.Uint16(p[14:16]))
	if ch < 1 || ch > 8 {
		return clock.ErrorAt(clock.ErrInvalidArgument, 2, "channel count %d outside 1..8", ch)
	}
	if bits != 16 && bits != 24 {
		return clock.ErrorAt(clock.ErrInvalidArgument, 14, "bits per sample %d (16 or 24 only)", bits)
	}
	if rate <= 0 || rate > 768000 {
		return clock.ErrorAt(clock.ErrInvalidArgument, 4, "sample rate %d invalid", rate)
	}
	if blockAlign != ch*bits/8 {
		return clock.ErrorAt(clock.ErrInvalidArgument, 12,
			"blockAlign %d != channels*bits/8 %d", blockAlign, ch*bits/8)
	}
	if byteRate != int64(rate)*int64(blockAlign) {
		return clock.ErrorAt(clock.ErrInvalidArgument, 8,
			"byteRate %d inconsistent with rate/blockAlign", byteRate)
	}
	h.Channels, h.SampleRate, h.BitsPerSample = ch, rate, bits
	h.BlockAlign, h.ByteRate = blockAlign, byteRate
	return nil
}
