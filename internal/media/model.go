package media

import (
	"crypto/sha256"
	"encoding/hex"
)

// PCMFormat is the validated integer-PCM subset of a WAV fmt chunk.
type PCMFormat struct {
	AudioFormat   uint16 `json:"audio_format"`
	Channels      uint16 `json:"channels"`
	SampleRate    uint32 `json:"sample_rate"`
	ByteRate      uint32 `json:"byte_rate"`
	BlockAlign    uint16 `json:"block_align"`
	BitsPerSample uint16 `json:"bits_per_sample"`
}

func (f PCMFormat) ChannelCount() int    { return int(f.Channels) }
func (f PCMFormat) BitDepth() int        { return int(f.BitsPerSample) }
func (f PCMFormat) Rate() int            { return int(f.SampleRate) }
func (f PCMFormat) FrameByteCount() int  { return int(f.BlockAlign) }
func (f PCMFormat) SampleByteCount() int { return int(f.BitsPerSample) / 8 }

// Validate enforces the explicit supported format envelope.
func (f PCMFormat) Validate() error {
	if f.AudioFormat != 1 {
		return NewError(CodeUnsupported, StageParse, -1, "only integer PCM format tag 1 is supported", nil)
	}
	if !SupportedChannels[int(f.Channels)] {
		return NewError(CodeUnsupported, StageParse, -1, "unsupported channel count", nil)
	}
	if !SupportedBitsPerSample[int(f.BitsPerSample)] || f.BitsPerSample%8 != 0 {
		return NewError(CodeUnsupported, StageParse, -1, "unsupported bits per sample", nil)
	}
	if !SupportedSampleRates[int(f.SampleRate)] {
		return NewError(CodeUnsupported, StageParse, -1, "unsupported sample rate", nil)
	}
	wantAlign := uint16(int(f.Channels) * int(f.BitsPerSample) / 8)
	if f.BlockAlign != wantAlign || f.BlockAlign == 0 {
		return NewError(CodeUnsupported, StageParse, -1, "invalid block alignment", nil)
	}
	wantByteRate := f.SampleRate * uint32(wantAlign)
	if f.ByteRate != wantByteRate {
		return NewError(CodeUnsupported, StageParse, -1, "invalid PCM byte rate", nil)
	}
	return nil
}

// RawBlock is one RIFF data chunk and its optional source timestamp.
type RawBlock struct {
	Index            int    `json:"index"`
	ChunkOffset      int64  `json:"chunk_offset"`
	DataOffset       int64  `json:"data_offset"`
	DeclaredByteSize uint32 `json:"declared_byte_size"`
	Data             []byte `json:"-"`
	TimestampPresent bool   `json:"timestamp_present"`
	TimestampNS      int64  `json:"timestamp_ns"`
	Truncated        bool   `json:"truncated"`
	CompleteFrames   int64  `json:"complete_frames"`
	TailBytes        int    `json:"tail_bytes"`
	CorruptOffset    int64  `json:"corrupt_offset"`
	CorruptionReason string `json:"corruption_reason,omitempty"`
}

// CanonicalBlock is a frame-aligned block on the repaired continuous timeline.
type CanonicalBlock struct {
	Index               int      `json:"index"`
	ChunkOffset         int64    `json:"chunk_offset"`
	OriginalOffset      int64    `json:"original_offset"`
	FrameCount          int64    `json:"frame_count"`
	StartFrame          int64    `json:"start_frame"`
	EndFrame            int64    `json:"end_frame"`
	StartNS             int64    `json:"start_ns"`
	EndNS               int64    `json:"end_ns"`
	AudioSHA256         string   `json:"audio_sha256"`
	OriginalTimestampNS *int64   `json:"original_timestamp_ns,omitempty"`
	TimestampPresent    bool     `json:"timestamp_present"`
	RepairCodes         []string `json:"repair_codes,omitempty"`
	Bad                 bool     `json:"bad"`
	CorruptOffset       int64    `json:"corrupt_offset,omitempty"`
	CorruptionReason    string   `json:"corruption_reason,omitempty"`
	PCM                 []byte   `json:"-"`
}

// SHA256Hex returns the lowercase hex SHA-256 of data.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
