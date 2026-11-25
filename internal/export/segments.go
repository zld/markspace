package export

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/zld/media-check/internal/fault"
	"github.com/zld/media-check/internal/mp4"
)

func exportSegments(segments []mp4.Segment, frames []FrameStat, opts Options) []SegmentStat {
	byIndex := make(map[int]FrameStat, len(frames))
	for _, f := range frames {
		byIndex[f.Index] = f
	}
	out := make([]SegmentStat, 0, len(segments))
	for _, seg := range segments {
		hashes := []string{}
		artifacts := []string{}
		for i := seg.FirstSampleIndex; i <= seg.LastSampleIndex; i++ {
			if f, ok := byIndex[i]; ok {
				hashes = append(hashes, f.SHA256)
				if f.Artifact != "" {
					artifacts = append(artifacts, f.Artifact)
				}
			}
		}
		materialHash := aggregate(hashes)
		canonical := fmt.Sprintf("%s|%d|%d|%s|%s", seg.ID, seg.TimelineStart, seg.TimelineEnd, seg.MaterialID, materialHash)
		sum := sha256.Sum256([]byte(canonical))
		out = append(out, SegmentStat{
			SegmentID: seg.ID, TrackID: seg.TrackID, MaterialID: seg.MaterialID, SHA256: hex.EncodeToString(sum[:]),
			MaterialSHA256: materialHash, FrameHashes: hashes,
			TimelineStartMS: seg.TimelineStart, TimelineEndMS: seg.TimelineEnd,
			Provenance: Provenance{
				InputID: opts.InputID, InputSHA256: opts.InputSHA, Stage: fault.StageExport,
				DerivedFrom: artifacts,
			},
		})
	}
	return out
}
