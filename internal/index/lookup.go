package index

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"media-archiver/internal/media"
)

// Hit identifies a covering block and zero-based frame offset inside it.
type Hit struct {
	Entry             Entry `json:"entry"`
	GlobalFrame       int64 `json:"global_frame"`
	FrameOffset       int64 `json:"frame_offset"`
	TimeNS            int64 `json:"time_ns"`
	BlockOriginalByte int64 `json:"block_original_byte"`
}

// LookupFrame returns the entry covering frame and the in-block frame offset.
func (i *Index) LookupFrame(frame int64) (Hit, error) {
	if frame < 0 || frame >= i.TotalFrames {
		return Hit{}, media.NewError(media.CodeValidation, media.StageIndex, -1,
			fmt.Sprintf("frame %d outside indexed range [0,%d)", frame, i.TotalFrames), nil)
	}
	n := len(i.Entries)
	pos := sort.Search(n, func(k int) bool { return frame < i.Entries[k].EndFrame })
	if pos == n || frame < i.Entries[pos].StartFrame {
		return Hit{}, media.NewError(media.CodeValidation, media.StageIndex, -1, "no index entry covers frame", nil)
	}
	e := i.Entries[pos]
	return Hit{
		Entry:             e,
		GlobalFrame:       frame,
		FrameOffset:       frame - e.StartFrame,
		TimeNS:            media.Timebase{SampleRate: i.SampleRate}.FramesToNS(frame),
		BlockOriginalByte: e.OriginalOffset,
	}, nil
}

// LookupTimeNS converts time using the index timebase, then performs lookup.
func (i *Index) LookupTimeNS(ns int64) (Hit, error) {
	tb, err := media.NewTimebase(i.SampleRate)
	if err != nil {
		return Hit{}, media.NewError(media.CodeInvalidConfig, media.StageIndex, -1, "invalid index sample rate", err)
	}
	return i.LookupFrame(tb.NSToFrame(ns))
}

// Load reads and structurally validates an index file.
func Load(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, media.NewError(media.CodeIndexCorrupt, media.StageIndex, -1, "read index", err)
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, media.NewError(media.CodeIndexCorrupt, media.StageIndex, -1, "parse index JSON", err)
	}
	if err := idx.Validate(); err != nil {
		return nil, err
	}
	return &idx, nil
}

// Validate checks version, totals, and entry continuity.
func (i *Index) Validate() error {
	if i.Version != IndexVersion {
		return media.NewError(media.CodeIndexCorrupt, media.StageIndex, -1, "unsupported index version", nil)
	}
	if !media.SupportedSampleRates[i.SampleRate] {
		return media.NewError(media.CodeIndexCorrupt, media.StageIndex, -1, "invalid sample rate in index", nil)
	}
	var frame int64
	for k, e := range i.Entries {
		if e.StartFrame != frame || e.EndFrame < e.StartFrame {
			return media.NewError(media.CodeIndexCorrupt, media.StageIndex, e.OriginalOffset,
				fmt.Sprintf("broken continuity at entry %d", k), nil)
		}
		if e.FrameCount != e.EndFrame-e.StartFrame {
			return media.NewError(media.CodeIndexCorrupt, media.StageIndex, e.OriginalOffset, "frame count mismatch", nil)
		}
		frame = e.EndFrame
	}
	if frame != i.TotalFrames {
		return media.NewError(media.CodeIndexCorrupt, media.StageIndex, -1, "index total frame mismatch", nil)
	}
	return nil
}
