package segment

import (
	"io"

	"github.com/abema/go-mp4"

	"mediqc/internal/fault"
)

// nonSyncSampleFlag is bit 16 of the ISO BMFF sample_flags field.
const nonSyncSampleFlag = 0x10000

// ScanKeyframes extracts sync-sample positions (0-based frame numbers)
// from an MP4 stream. It prefers the stss index; when the index is
// missing it scans trun sample flags and marks the source accordingly.
func ScanKeyframes(r io.ReadSeeker) ([]int64, KeyframeSource, error) {
	var stss []int64
	var scanned []int64
	var sawTrun bool
	sampleNo := int64(0)

	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(),
			mp4.BoxTypeMinf(), mp4.BoxTypeStbl(),
			mp4.BoxTypeMoof(), mp4.BoxTypeTraf():
			return h.Expand()
		case mp4.BoxTypeStss():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			for _, n := range box.(*mp4.Stss).SampleNumber {
				stss = append(stss, int64(n)-1) // stss is 1-based
			}
		case mp4.BoxTypeTrun():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			trun := box.(*mp4.Trun)
			sawTrun = true
			for i, e := range trun.Entries {
				sync := true
				if trun.CheckFlag(0x000400) { // sample-flags-present
					sync = e.SampleFlags&nonSyncSampleFlag == 0
				} else if i == 0 && trun.CheckFlag(0x000004) {
					sync = trun.FirstSampleFlags&nonSyncSampleFlag == 0
				}
				if sync {
					scanned = append(scanned, sampleNo)
				}
				sampleNo++
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, "", fault.New(fault.CodeTruncatedInput, 0, -1,
			"mp4 box walk failed: "+err.Error(), "re-deliver the complete asset")
	}
	if len(stss) > 0 {
		return stss, SourceIndex, nil
	}
	if sawTrun && len(scanned) > 0 {
		return scanned, SourceScan, nil
	}
	return nil, "", fault.New(fault.CodeMissingKeyframe, 0, -1,
		"no stss index and no trun samples to scan",
		"re-deliver the asset with a sync-sample index")
}
