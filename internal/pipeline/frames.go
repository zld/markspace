package pipeline

import (
	"archive/internal/clock"
	"archive/internal/manifest"
	"archive/internal/render"
	"archive/internal/timeline"
)

func renderTracks(opts Options, tl *timeline.Timeline) ([]manifest.FrameEntry, map[string][]byte, error) {
	var frames []manifest.FrameEntry
	pngs := make(map[string][]byte)
	for _, tr := range tl.Tracks {
		res := tr.Resolution
		limit := opts.FrameLimit
		if limit <= 0 {
			limit = tr.FrameCount()
		}
		idx := 0
		for _, clip := range tr.Clips {
			for _, ts := range timeline.FrameTimes(tr, clip) {
				if idx >= limit {
					break
				}
				ref, err := render.Reference(res.StoredWidth, res.StoredHeight, render.Seed(tr.ID), idx)
				if err != nil {
					return nil, nil, err
				}
				out, err := render.ApplyTransform(ref, res)
				if err != nil {
					return nil, nil, err
				}
				png, err := render.EncodePNG(out)
				if err != nil {
					return nil, nil, err
				}
				name := manifest.FrameFileName(tr.ID, idx)
				pngs[name] = png
				frames = append(frames, manifest.FrameEntry{
					TrackID:       tr.ID,
					TimestampNS:   ts,
					FrameIndex:    idx,
					Width:         out.Bounds().Dx(),
					Height:        out.Bounds().Dy(),
					Crop:          render.Crop(res),
					Rotation:      res.Rotation,
					ColorResolved: res.ColorResolved,
					File:          name,
				})
				idx++
			}
		}
	}
	if len(frames) == 0 {
		return nil, nil, clock.New(clock.KindInvalidMetadata, "no frames rendered")
	}
	return frames, pngs, nil
}
