package kernel

import (
	"fmt"
	"os"
	"path/filepath"

	"mediad/internal/mp4index"
)

// buildInitSegments writes an ISO-BMFF init segment per rendition and verifies
// with go-mp4 that each describes the expected millisecond time base.
func buildInitSegments(work string, reps []*Representation, cfg Config) (map[string]string, error) {
	out := make(map[string]string, len(reps))
	durationMS := uint64(reps[0].Duration / 1_000_000)
	for i, r := range reps {
		tracks := []mp4index.TrackInfo{
			{TrackID: 1, Timescale: 90000, DurationMS: durationMS, Handler: "vide", Width: 640, Height: 360, Codec: "V_VP9"},
			{TrackID: 2, Timescale: 48000, DurationMS: durationMS, Handler: "soun", Codec: "A_OPUS"},
		}
		data, err := mp4index.BuildInitSegment(tracks)
		if err != nil {
			return nil, internalErr("mp4index", err)
		}
		movieScale, trackScales, err := mp4index.ParseInitSegment(data)
		if err != nil {
			return nil, internalErr("mp4index", err)
		}
		if movieScale != 1000 || trackScales[90000] != 1 || trackScales[48000] != 1 {
			return nil, internalErr("mp4index", fmt.Errorf("rendition %s time base %d %v", r.ID, movieScale, trackScales))
		}
		path := filepath.Join(work, fmt.Sprintf("init-%d.mp4", i))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return nil, internalErr("write", err)
		}
		out[r.ID] = path
	}
	return out, nil
}
