package pipeline

import (
	"fmt"
	"strings"

	"media-archiver/internal/index"
	"media-archiver/internal/media"
	"media-archiver/internal/repair"
	"media-archiver/internal/samplegen"
	"media-archiver/internal/verify"
)

// Report is the machine-readable result rendered by the terminal adapter.
type Report struct {
	WorkDir             string              `json:"workdir"`
	Status              string              `json:"status"`
	TargetBitrateBPS    int64               `json:"target_bitrate_bps"`
	ActualBitrateBPS    float64             `json:"actual_bitrate_bps"`
	BitrateDeviationPct float64             `json:"bitrate_deviation_pct"`
	Frames              int64               `json:"frames"`
	Channels            int                 `json:"channels"`
	Peak                int64               `json:"peak"`
	ClippedSamples      int64               `json:"clipped_samples"`
	BadBlocks           int                 `json:"bad_blocks"`
	ManifestAlignmentNS int64               `json:"manifest_alignment_ns"`
	IndexEntries        int                 `json:"index_entries"`
	IndexSHA256         string              `json:"index_sha256,omitempty"`
	ManifestSHA256      string              `json:"manifest_sha256,omitempty"`
	AudioSHA256         string              `json:"audio_sha256,omitempty"`
	InputSHA256         string              `json:"input_sha256,omitempty"`
	RepairCount         int                 `json:"repair_count"`
	Repairs             []repair.Decision   `json:"repairs,omitempty"`
	Lookup              *index.Hit          `json:"lookup,omitempty"`
	Expected            *samplegen.Expected `json:"expected,omitempty"`
	IndexRebuilt        bool                `json:"index_rebuilt,omitempty"`
	ReusedStages        []string            `json:"reused_stages,omitempty"`
	RecomputedStages    []string            `json:"recomputed_stages,omitempty"`
	CorruptOffset       int64               `json:"corrupt_offset,omitempty"`
	Stage               media.Stage         `json:"stage,omitempty"`
	ErrorCode           media.ErrorCode     `json:"error_code,omitempty"`
	Stats               *verify.Stats       `json:"stats,omitempty"`
}

// PrintKeyValues emits the stable acceptance key=value surface.
func (r *Report) PrintKeyValues() {
	if r == nil {
		return
	}
	lines := []string{
		fmt.Sprintf("status=%s", r.Status),
		fmt.Sprintf("target_bitrate_bps=%d", r.TargetBitrateBPS),
		fmt.Sprintf("actual_bitrate_bps=%.3f", r.ActualBitrateBPS),
		fmt.Sprintf("bitrate_deviation_pct=%.6f", r.BitrateDeviationPct),
		fmt.Sprintf("frames=%d", r.Frames),
		fmt.Sprintf("channels=%d", r.Channels),
		fmt.Sprintf("peak=%d", r.Peak),
		fmt.Sprintf("clipped_samples=%d", r.ClippedSamples),
		fmt.Sprintf("bad_blocks=%d", r.BadBlocks),
		fmt.Sprintf("manifest_alignment_ns=%d", r.ManifestAlignmentNS),
		fmt.Sprintf("index_entries=%d", r.IndexEntries),
	}
	if r.IndexSHA256 != "" {
		lines = append(lines, "index_sha256="+r.IndexSHA256)
	}
	if r.ManifestSHA256 != "" {
		lines = append(lines, "manifest_sha256="+r.ManifestSHA256)
	}
	if r.AudioSHA256 != "" {
		lines = append(lines, "audio_sha256="+r.AudioSHA256)
	}
	lines = append(lines, fmt.Sprintf("repair_count=%d", r.RepairCount))
	if r.IndexRebuilt {
		lines = append(lines, "index_rebuilt=true")
	}
	if len(r.ReusedStages) > 0 {
		lines = append(lines, "reused_stages="+strings.Join(r.ReusedStages, ","))
	}
	if len(r.RecomputedStages) > 0 {
		lines = append(lines, "recomputed_stages="+strings.Join(r.RecomputedStages, ","))
	}
	if r.Lookup != nil {
		lines = append(lines,
			fmt.Sprintf("lookup_frame=%d", r.Lookup.GlobalFrame),
			fmt.Sprintf("lookup_block=%d", r.Lookup.Entry.BlockIndex),
			fmt.Sprintf("lookup_frame_offset=%d", r.Lookup.FrameOffset),
			fmt.Sprintf("lookup_original_offset=%d", r.Lookup.BlockOriginalByte),
		)
	}
	for i, d := range r.Repairs {
		origNS := "missing"
		if d.OriginalStartNS != nil {
			origNS = fmt.Sprintf("%d", *d.OriginalStartNS)
		}
		lines = append(lines,
			fmt.Sprintf("repair.%d.block=%d", i, d.BlockIndex),
			fmt.Sprintf("repair.%d.kind=%s", i, d.Kind),
			fmt.Sprintf("repair.%d.original_start_ns=%s", i, origNS),
			fmt.Sprintf("repair.%d.repaired_start_ns=%d", i, d.RepairedStartNS),
			fmt.Sprintf("repair.%d.original_start_frame=%s", i, ptrFrame(d.OriginalStartFrame)),
			fmt.Sprintf("repair.%d.repaired_start_frame=%d", i, d.RepairedStartFrame),
		)
	}
	if r.ErrorCode != "" {
		lines = append(lines,
			fmt.Sprintf("error_code=%s", r.ErrorCode),
			fmt.Sprintf("stage=%s", r.Stage),
			fmt.Sprintf("corrupt_offset=%d", r.CorruptOffset),
		)
	}
	fmt.Println(strings.Join(lines, "\n"))
}

func ptrFrame(v *int64) string {
	if v == nil {
		return "missing"
	}
	return fmt.Sprintf("%d", *v)
}
