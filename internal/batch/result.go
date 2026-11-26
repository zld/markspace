package batch

import (
	"mediarepair/internal/cache"
	"mediarepair/internal/fsx"
	"mediarepair/internal/index"
	"mediarepair/internal/probe"
	"mediarepair/internal/repair"
)

type Result struct {
	InputFingerprint string           `json:"input_fingerprint"`
	MetadataFP       string           `json:"metadata_fingerprint"`
	PackagingFP      string           `json:"packaging_fingerprint"`
	Repair           *repair.Report   `json:"repair"`
	Index            *index.Index     `json:"index"`
	IndexSHA256      string           `json:"index_sha256"`
	SegmentHashes    []string         `json:"segment_sha256"`
	FrameHashes      []string         `json:"frame_sha256"`
	Pixels           probe.PixelStats `json:"-"`
	PixelMeanRGB     string           `json:"pixel_mean_rgb"`
	Audio            *probe.WAVStats  `json:"audio,omitempty"`
	AlignMaxUS       float64          `json:"align_max_us"`
	CacheCounters    cache.Counters   `json:"cache_counters"`
	Resumed          bool             `json:"resumed"`
	Checkpoint       *Checkpoint      `json:"checkpoint"`
	SampleCount      int              `json:"sample_count"`
}

func (r *Result) finalizeVisualMean() {
	red, green, blue := r.Pixels.Means()
	r.PixelMeanRGB = probe.FormatMeanRGB(red, green, blue)
}

func readAndAlignAudio(root, audioRel string, rep *repair.Report, blockSize, numBlocks int, r *Result) error {
	if audioRel == "" {
		return nil
	}
	audioFull, err := fsx.SafeJoin(root, audioRel)
	if err != nil {
		return err
	}
	audio, err := probe.ReadWAV(audioFull)
	if err != nil {
		return err
	}
	r.Audio = audio

	visual := make([]int64, 0, numBlocks)
	positions := make([]int, 0, numBlocks)
	for b := 0; b < numBlocks; b++ {
		fi := b * blockSize
		if fi < len(rep.Frames) {
			visual = append(visual, rep.Frames[fi].RepairedTimeUS)
		}
	}
	onsets := probe.DetectOnsets(audio.Samples, 0.49, audio.SampleRate/8)
	for i := 0; i < len(visual) && i < len(onsets); i++ {
		positions = append(positions, onsets[i])
	}
	r.AlignMaxUS, _ = probe.AlignmentErrorUS(visual, positions, audio.SampleRate)
	return nil
}
