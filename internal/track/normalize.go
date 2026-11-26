// Package track defines the canonical sparse audio track shared by RTP,
// validation, derivation, manifests and metrics.
package track

import (
	"io"

	"mediacheck/internal/gap"
	"mediacheck/internal/timebase"
	"mediacheck/internal/wav"
)

// ChannelMapping maps canonical output channel i to Source[i] source channel.
type ChannelMapping struct {
	SourceChannels int   `json:"source_channels"`
	OutputChannels int   `json:"output_channels"`
	Order          []int `json:"order"`
}

func DefaultMapping(channels int) ChannelMapping {
	order := make([]int, channels)
	for i := range order {
		order[i] = i
	}
	return ChannelMapping{SourceChannels: channels, OutputChannels: channels, Order: order}
}

func (m ChannelMapping) MapFrame(src []int32) ([]int32, error) {
	if len(src) != m.SourceChannels {
		return nil, timebase.NewError(timebase.CodeTrack, "source frame width does not match mapping")
	}
	out := make([]int32, m.OutputChannels)
	for i, srcCh := range m.Order {
		if i >= m.OutputChannels || srcCh < 0 || srcCh >= m.SourceChannels {
			return nil, timebase.NewError(timebase.CodeTrack, "invalid channel mapping index")
		}
		out[i] = src[srcCh]
	}
	return out, nil
}

// Chunk is a contiguous run of interleaved canonical int32 samples.
type Chunk struct {
	Index        int     `json:"index"`
	StartSample  int64   `json:"start_sample"`
	FrameCount   int64   `json:"frame_count"`
	SourceOffset int64   `json:"source_offset"`
	ExtSeqStart  int64   `json:"ext_seq_start,omitempty"`
	ExtSeqEnd    int64   `json:"ext_seq_end,omitempty"`
	Samples      []int32 `json:"-"`
}

func (c Chunk) EndSample() int64 { return c.StartSample + c.FrameCount }

// Track is a sparse timeline: Chunks contain real samples and Gaps contain
// explicitly absent intervals. Missing audio is never zero-filled here.
type Track struct {
	ID          string         `json:"track_id"`
	SourceSHA   string         `json:"source_sha,omitempty"`
	SampleRate  int            `json:"sample_rate"`
	Channels    int            `json:"channels"`
	SourceBits  int            `json:"source_bits"`
	Mapping     ChannelMapping `json:"channel_mapping"`
	Chunks      []Chunk        `json:"chunks"`
	Gaps        []gap.Gap      `json:"gaps"`
	TimelineEnd int64          `json:"timeline_end_samples"`
	Delivered   int64          `json:"delivered_samples"`
}

func New(id string, sampleRate, channels, sourceBits int, mapping *ChannelMapping) (*Track, error) {
	if sampleRate <= 0 || channels < 1 || channels > wav.MaxChannels {
		return nil, timebase.NewError(timebase.CodeTrack, "invalid track rate or channels")
	}
	m := DefaultMapping(channels)
	if mapping != nil {
		if mapping.OutputChannels == 0 {
			mapping.OutputChannels = channels
		}
		if len(mapping.Order) != mapping.OutputChannels {
			return nil, timebase.NewError(timebase.CodeTrack, "channel mapping order length mismatch")
		}
		if mapping.SourceChannels == 0 {
			mapping.SourceChannels = channels
		}
		for _, ch := range mapping.Order {
			if ch < 0 || ch >= mapping.SourceChannels {
				return nil, timebase.NewError(timebase.CodeTrack, "channel mapping refers outside source")
			}
		}
		m = *mapping
	}
	return &Track{ID: id, SampleRate: sampleRate, Channels: m.OutputChannels, SourceBits: sourceBits, Mapping: m}, nil
}

// AppendChunk appends a contiguous, non-overlapping chunk in sample order.
func (t *Track) AppendChunk(startSample int64, samples []int32, sourceOffset int64) error {
	if startSample < 0 {
		return timebase.NewError(timebase.CodeTrack, "negative chunk start")
	}
	if len(samples)%t.Channels != 0 {
		return timebase.At(timebase.CodeAlignment, sourceOffset, "chunk length is not frame aligned")
	}
	frames := int64(len(samples) / t.Channels)
	if frames == 0 {
		return nil
	}
	if n := len(t.Chunks); n > 0 {
		last := t.Chunks[n-1]
		if startSample < last.EndSample() {
			return timebase.At(timebase.CodeTrack, sourceOffset,
				"chunk overlap: start=%d previous_end=%d", startSample, last.EndSample())
		}
	}
	t.Chunks = append(t.Chunks, Chunk{
		Index:        len(t.Chunks),
		StartSample:  startSample,
		FrameCount:   frames,
		SourceOffset: sourceOffset,
		Samples:      samples,
	})
	end := startSample + frames
	if end > t.TimelineEnd {
		t.TimelineEnd = end
	}
	t.Delivered += frames
	return nil
}

// AppendPCM decodes little-endian packed PCM and appends it at startSample.
func (t *Track) AppendPCM(startSample int64, pcm []byte, bits int, sourceOffset int64) error {
	if bits != 16 && bits != 24 && bits != 32 {
		return timebase.NewError(timebase.CodeTrack, "unsupported PCM bit depth")
	}
	frameBytes := t.Mapping.SourceChannels * (bits / 8)
	if frameBytes == 0 || len(pcm)%frameBytes != 0 {
		return timebase.At(timebase.CodeAlignment, sourceOffset, "PCM payload not aligned to source frame")
	}
	frames := len(pcm) / frameBytes
	out := make([]int32, 0, frames*t.Channels)
	frame := make([]int32, t.Mapping.SourceChannels)
	for f := 0; f < frames; f++ {
		base := f * frameBytes
		for c := 0; c < t.Mapping.SourceChannels; c++ {
			s, err := wav.DecodeSample(pcm[base+c*(bits/8):], bits)
			if err != nil {
				return err
			}
			frame[c] = s
		}
		mapped, err := t.Mapping.MapFrame(frame)
		if err != nil {
			return err
		}
		out = append(out, mapped...)
	}
	return t.AppendChunk(startSample, out, sourceOffset)
}

// AddGaps records the authoritative gap list and extends timeline end.
func (t *Track) AddGaps(gaps []gap.Gap) {
	t.Gaps = append(t.Gaps, gaps...)
	for _, g := range gaps {
		if g.EndSample > t.TimelineEnd {
			t.TimelineEnd = g.EndSample
		}
	}
}

// ChunkSink receives bounded normalized chunks while streaming a source.
type ChunkSink func(Chunk) error

// StreamWAV validates and decodes a WAV source in bounded chunkFrames groups.
// The returned Track has metadata/mapping but chunk ownership stays with sink.
func StreamWAV(id string, ra io.ReaderAt, size int64, chunkFrames int, sink ChunkSink) (*Track, error) {
	wr, err := wav.OpenAt(ra, size)
	if err != nil {
		return nil, err
	}
	m := wr.Meta()
	t, err := New(id, m.SampleRate, m.Channels, m.BitsPerSample, nil)
	if err != nil {
		return nil, err
	}
	if chunkFrames <= 0 || chunkFrames > 4096 {
		chunkFrames = 1024
	}
	frameBuf := make([]byte, m.BlockAlign)
	for {
		samples := make([]int32, 0, chunkFrames*m.Channels)
		frames := 0
		for frames < chunkFrames {
			if err := wr.ReadFrame(frameBuf); err == io.EOF {
				break
			} else if err != nil {
				return nil, err
			}
			for c := 0; c < m.Channels; c++ {
				s, derr := wav.DecodeSample(frameBuf[c*(m.BitsPerSample/8):], m.BitsPerSample)
				if derr != nil {
					return nil, derr
				}
				samples = append(samples, s)
			}
			frames++
		}
		if frames == 0 {
			break
		}
		start := wr.FrameIndex() - int64(frames)
		offset := m.DataOffset + start*int64(m.BlockAlign)
		if err := t.AppendChunk(start, samples, offset); err != nil {
			return nil, err
		}
		if sink != nil {
			if err := sink(t.Chunks[len(t.Chunks)-1]); err != nil {
				return nil, err
			}
		}
	}
	// Streaming sink owns chunks; keep t.Chunks anyway for small acceptance
	// sources. Production callers may replace sink with disk persistence.
	return t, nil
}

// ReadWAV is the bounded-convenience collector used by tests and fixtures.
func ReadWAV(id string, ra io.ReaderAt, size int64) (*Track, error) {
	return StreamWAV(id, ra, size, 1024, nil)
}
