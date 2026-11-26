package engine

import (
	"fmt"

	"mediatrace/internal/audio"
	"mediatrace/internal/cache"
	"mediatrace/internal/ebml"
	"mediatrace/internal/output"
	"mediatrace/internal/timeline"
)

// frameAt converts a nanosecond timestamp to a 48 kHz frame index using
// round-to-nearest. Every block is assigned a frame interval even when its
// event mapping has a gap, because the audio samples still exist.
func frameAt(timeNS int64) int64 {
	rate := int64(audio.TargetRateHz)
	if timeNS >= 0 {
		return (timeNS*rate + timeline.NanosecondsPerSecond/2) / timeline.NanosecondsPerSecond
	}
	return -(((-timeNS)*rate + timeline.NanosecondsPerSecond/2) / timeline.NanosecondsPerSecond)
}

func buildManifest(p Plan, doc *ebml.Document, inputSHA []byte, inputSize int64,
	state cache.State, entries []ebml.IndexEntry, views []timeline.BlockView,
	mappings []timeline.Mapping, deliveries []output.TrackDelivery, qualityFail bool,
) output.Manifest {
	blocks := make([]output.BlockRecord, 0, len(mappings))
	mappedCount := 0
	var maxErr int64
	for i, m := range mappings {
		rec := output.BlockRecord{
			TrackNo:     m.TrackNo,
			BlockTimeNS: m.BlockTimeNS,
			EventTimeNS: m.EventAtNS,
			EventLine:   m.EventLine,
			TimeErrorNS: m.TimeErrorNS,
			Gap:         string(m.Gap),
			GapDetail:   m.Detail,
		}
		if i < len(entries) {
			rec.ElemOffset = entries[i].ElemOffset
			rec.DataOffset = entries[i].DataOffset
			rec.DataSize = entries[i].DataSize
		}
		rec.OutputFrameStart = frameAt(m.BlockTimeNS)
		rec.OutputFrameEnd = nextFrameStart(views, i)
		if m.Gap == timeline.GapNone {
			mappedCount++
			if d := timeline.Abs(m.TimeErrorNS, 0); d > maxErr {
				maxErr = d
			}
		}
		blocks = append(blocks, rec)
	}
	m := output.Manifest{
		Version:    output.ManifestVersion,
		CacheState: string(state),
		Input: output.InputSummary{
			Path:          p.InputPath,
			SHA256:        hexSHA(inputSHA),
			SizeBytes:     inputSize,
			DocType:       doc.DocType,
			TimecodeScale: doc.TimecodeScale,
			TrackCount:    len(doc.Tracks),
			BlockCount:    doc.BlockCount,
		},
		Blocks:     blocks,
		Tracks:     deliveries,
		Mapped:     fmt.Sprintf("%d/%d", mappedCount, len(mappings)),
		MaxErrorNS: maxErr,
	}
	if qualityFail {
		m.Failures = append(m.Failures, output.FailureRecord{
			Code:   "QUALITY_BUDGET_EXHAUSTED",
			Offset: -1,
			Detail: "no AAC candidate met the -16 LUFS / -1 dBTP gate within budget",
		})
	}
	return m
}

// nextFrameStart gives the exclusive frame boundary for a block: the start
// frame of the following block on the same track, or zero at track end where
// the encoded duration is reported separately by the MP4 sample table.
func nextFrameStart(views []timeline.BlockView, i int) int64 {
	cur := views[i]
	for j := i + 1; j < len(views); j++ {
		if views[j].TrackNo == cur.TrackNo {
			return frameAt(views[j].TimeNS)
		}
	}
	return 0
}

func hexSHA(b []byte) string {
	const hexChars = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, x := range b {
		out[i*2] = hexChars[x>>4]
		out[i*2+1] = hexChars[x&0x0F]
	}
	return string(out)
}
