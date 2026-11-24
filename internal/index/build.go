package index

import (
	"fmt"

	"media-archiver/internal/media"
)

const IndexVersion = 1

// Entry is one random-access point for a canonical block.
type Entry struct {
	BlockIndex     int    `json:"block_index"`
	OriginalOffset int64  `json:"original_offset"`
	PCMOffset      int64  `json:"pcm_offset"`
	StartFrame     int64  `json:"start_frame"`
	EndFrame       int64  `json:"end_frame"`
	FrameCount     int64  `json:"frame_count"`
	StartNS        int64  `json:"start_ns"`
	EndNS          int64  `json:"end_ns"`
	AudioSHA256    string `json:"audio_sha256"`
	Bad            bool   `json:"bad"`
}

// Index is the deterministic frame/time random-access index.
type Index struct {
	Version     int     `json:"version"`
	SampleRate  int     `json:"sample_rate"`
	TotalFrames int64   `json:"total_frames"`
	Entries     []Entry `json:"entries"`
}

type BuildOptions struct {
	MaxEntries int
}

func DefaultBuildOptions() BuildOptions { return BuildOptions{MaxEntries: 1_000_000} }

// Build reconstructs the index from canonical frame-aligned blocks.
func Build(blocks []media.CanonicalBlock, sampleRate int, opts BuildOptions) (*Index, error) {
	if opts.MaxEntries <= 0 {
		opts = DefaultBuildOptions()
	}
	if len(blocks) > opts.MaxEntries {
		return nil, media.NewError(media.CodeBlockTooLarge, media.StageIndex, -1,
			fmt.Sprintf("index entries %d exceed memory limit %d", len(blocks), opts.MaxEntries), nil)
	}
	if !media.SupportedSampleRates[sampleRate] {
		return nil, media.NewError(media.CodeInvalidConfig, media.StageIndex, -1, "unsupported sample rate", nil)
	}
	idx := &Index{Version: IndexVersion, SampleRate: sampleRate, Entries: make([]Entry, 0, len(blocks))}
	var pcmOffset int64
	var expectedFrame int64
	for _, b := range blocks {
		if b.StartFrame != expectedFrame {
			return nil, media.NewError(media.CodeValidation, media.StageIndex, b.OriginalOffset,
				fmt.Sprintf("non-contiguous block %d: expected frame %d got %d", b.Index, expectedFrame, b.StartFrame), nil)
		}
		if b.EndFrame < b.StartFrame {
			return nil, media.NewError(media.CodeValidation, media.StageIndex, b.OriginalOffset, "negative frame count", nil)
		}
		idx.Entries = append(idx.Entries, Entry{
			BlockIndex:     b.Index,
			OriginalOffset: b.OriginalOffset,
			PCMOffset:      pcmOffset,
			StartFrame:     b.StartFrame,
			EndFrame:       b.EndFrame,
			FrameCount:     b.FrameCount,
			StartNS:        b.StartNS,
			EndNS:          b.EndNS,
			AudioSHA256:    b.AudioSHA256,
			Bad:            b.Bad,
		})
		idx.TotalFrames = b.EndFrame
		expectedFrame = b.EndFrame
		pcmOffset += int64(len(b.PCM))
	}
	return idx, nil
}

// CanonicalJSON returns byte-stable index JSON.
func (i *Index) CanonicalJSON() ([]byte, error) {
	return media.MarshalCanonicalJSON(i)
}
