package pipeline

import (
	"encoding/json"

	"archivecheck/internal/media"
	"archivecheck/internal/quality"
)

// qualityMeasure re-runs the bitrate measurement for the final report.
// All inputs remain integer; only the presentation layer converts to a
// fixed-point percentage below.
func qualityMeasure(segments []*media.Segment,
	targets map[uint32]int) ([]quality.SegmentBitrate, quality.BitrateSummary) {
	return quality.MeasureBitrate(segments, targets)
}

// bpToPercent converts basis points (1bp = 0.01%) to a percentage with
// two fractional digits using fixed-point integer math.
func bpToPercent(bp int) float64 {
	// bp/100 = percent*100, so percent = bp/10000.
	return float64(bp) / 10000.0
}

// microsToMs converts integer microseconds to milliseconds.
func microsToMs(micros int64) float64 {
	return float64(micros) / 1000.0
}

func jsonUnmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

func jsonMarshalIndent(v interface{}) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}
