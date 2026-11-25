// Package fixture deterministically generates self-contained WAV/ABLK and
// structural MP4 samples. No external media files or randomness are used.
package fixture

import (
	"encoding/binary"
	"os"
	"path/filepath"

	"assetcheck/internal/wav"
)

// Spec describes one generated WAV sample.
type Spec struct {
	Name              string
	Frames            int64
	Channels          int
	SampleRate        int
	Bits              int
	BlockFrames       int
	Title             string
	CorruptBlockIndex int   // -1 = none
	TruncateTailBytes int   // cut this many bytes off the finished file
	DuplicateBoundary bool  // duplicate one ABLK start frame
}

// GoodSpec is the acceptance baseline: 2 s, 48 kHz, 6 channels, PCM16.
func GoodSpec() Spec {
	return Spec{
		Name: "good.wav", Frames: 96000, Channels: 6, SampleRate: 48000,
		Bits: 16, BlockFrames: 512, Title: "assetcheck-good",
		CorruptBlockIndex: -1,
	}
}

// BuildWAV serializes a deterministic WAV/ABLK sample.
func BuildWAV(s Spec) []byte {
	if s.Channels == 0 {
		s.Channels = 6
	}
	if s.SampleRate == 0 {
		s.SampleRate = 48000
	}
	if s.Bits == 0 {
		s.Bits = 16
	}
	if s.BlockFrames == 0 {
		s.BlockFrames = 512
	}
	align := s.Channels * s.Bits / 8
	pcm := make([]byte, s.Frames*int64(align))
	// Deterministic non-silent pattern; every block has non-trivial CRC input.
	for f := int64(0); f < s.Frames; f++ {
		for c := 0; c < s.Channels; c++ {
			v := int16(((f*31 + int64(c)*17 + f/37) % 65531) - 32765)
			off := f*int64(align) + int64(c*2)
			binary.LittleEndian.PutUint16(pcm[off:off+2], uint16(v))
		}
	}
	// Integrity table is computed over the pristine PCM first; the optional
	// corruption below then simulates post-delivery damage the CRC catches.
	table := wav.BuildBlockTable(s.Frames, s.BlockFrames, align, pcm)
	if s.CorruptBlockIndex >= 0 {
		start := int64(s.CorruptBlockIndex) * int64(s.BlockFrames) * int64(align)
		for i := start; i < start+32 && i < int64(len(pcm)); i++ {
			pcm[i] ^= 0xFF
		}
	}
	if s.DuplicateBoundary && len(table.Entries) > 4 {
		dup := table.Entries[3]
		dup.Index = uint32(len(table.Entries))
		table.Entries = append(table.Entries, wav.BlockEntry{})
		copy(table.Entries[5:], table.Entries[4:])
		table.Entries[4] = dup
	}
	ablkPayload := wav.EncodeBlockTable(table)
	fmtPayload := buildFmt(s)
	infoPayload := buildInfo(s.Title)

	var b []byte
	b = append(b, "RIFF"...)
	b = appendU32(b, 0)
	b = append(b, "WAVE"...)
	b = appendChunk(b, "fmt ", fmtPayload)
	b = appendChunk(b, wav.BlockTableID, ablkPayload)
	b = appendChunk(b, "LIST", infoPayload)
	b = appendChunk(b, "data", pcm)
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)-8))

	if s.TruncateTailBytes > 0 && s.TruncateTailBytes < len(b) {
		b = b[:len(b)-s.TruncateTailBytes]
	}
	return b
}

func buildFmt(s Spec) []byte {
	p := make([]byte, 16)
	align := s.Channels * s.Bits / 8
	binary.LittleEndian.PutUint16(p[0:2], 1)
	binary.LittleEndian.PutUint16(p[2:4], uint16(s.Channels))
	binary.LittleEndian.PutUint32(p[4:8], uint32(s.SampleRate))
	binary.LittleEndian.PutUint32(p[8:12], uint32(s.SampleRate*align))
	binary.LittleEndian.PutUint16(p[12:14], uint16(align))
	binary.LittleEndian.PutUint16(p[14:16], uint16(s.Bits))
	return p
}

func buildInfo(title string) []byte {
	if title == "" {
		title = "assetcheck"
	}
	t := append([]byte(title), 0) // NUL terminated
	out := []byte("INFO")
	out = append(out, "INAM"...)
	out = appendU32(out, uint32(len(t)))
	out = append(out, t...)
	if len(t)&1 == 1 {
		out = append(out, 0)
	}
	return out
}

func appendU32(b []byte, v uint32) []byte {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	return append(b, buf[:]...)
}

func appendChunk(b []byte, id string, payload []byte) []byte {
	b = append(b, id...)
	b = appendU32(b, uint32(len(payload)))
	b = append(b, payload...)
	if len(payload)&1 == 1 {
		b = append(b, 0)
	}
	return b
}

// NamedFixtures returns the full acceptance sample set.
func NamedFixtures() map[string][]byte {
	good := GoodSpec()
	out := map[string][]byte{}
	out["good.wav"] = BuildWAV(good)

	trunc := good
	trunc.Name = "truncated.wav"
	trunc.TruncateTailBytes = 4096
	out["truncated.wav"] = BuildWAV(trunc)

	corrupt := good
	corrupt.Name = "corrupt.wav"
	corrupt.CorruptBlockIndex = 10
	out["corrupt.wav"] = BuildWAV(corrupt)

	out["replaced.wav"] = BuildWAV(good)
	out["container.wav"] = BuildWAV(good)
	out["metadata.wav"] = BuildWAV(good)

	dup := good
	dup.Name = "dupbound.wav"
	dup.DuplicateBoundary = true
	out["dupbound.wav"] = BuildWAV(dup)

	out["badheader.wav"] = []byte("RIFF\x10\x00\x00\x00WAV") // 11 bytes: truncated header
	return out
}

// Generate writes every named fixture into dir and returns sorted paths.
func Generate(dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	files := NamedFixtures()
	paths := make([]string, 0, len(files))
	for name, data := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, nil
}
