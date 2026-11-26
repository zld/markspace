package task

import (
	"assetcheck/internal/analyze"
	"assetcheck/internal/manifest"
	"assetcheck/internal/media"
)

func later(a, b media.Rational) media.Rational {
	if media.Sub(a, b).N >= 0 {
		return a
	}
	return b
}

func buildManifestRequest(in Input, contentSHA string, rep *analyze.Report) manifest.Request {
	var duration = media.Zero
	tracks := make([]manifest.TrackInput, 0, len(rep.Tracks))
	for _, t := range rep.Tracks {
		if t.LastEnd.D > 0 {
			duration = later(duration, t.LastEnd)
		}
		tracks = append(tracks, manifest.TrackInput{
			TrackID:    t.TrackID,
			Language:   t.Language,
			TrackType:  string(t.Kind),
			Blocks:     t.Blocks,
			AlignMaxMS: t.AlignMaxMS,
			TargetBPS:  t.TargetBPS,
			ActualBPS:  t.ActualBPS,
			DeltaPct:   t.DeltaPct,
			PSNRDB:     t.PSNRDB,
		})
	}

	cues := make([]manifest.Cue, 0, len(rep.Cues))
	for _, c := range rep.Cues {
		cues = append(cues, manifest.Cue{
			CueID:    c.CueID,
			Language: c.Language,
			TrackID:  c.TrackID,
			Start:    c.Start,
			End:      c.End,
			Text:     c.Text,
		})
	}

	return manifest.Request{
		AssetID:           in.AssetID,
		ContentSHA256:     contentSHA,
		AnalyzerVersion:   analyze.Version,
		RequiredLanguages: append([]string(nil), in.RequiredLanguages...),
		Duration:          duration,
		Metrics: manifest.Metrics{
			TargetBPS:  rep.TargetBPS,
			ActualBPS:  rep.ActualBPS,
			DeltaPct:   rep.DeltaPct,
			PSNRDB:     rep.PSNRDB,
			AlignMaxMS: rep.AlignMaxMS,
		},
		Tracks:       tracks,
		Cues:         cues,
		ExpectedCues: append([]manifest.ExpectedCue(nil), in.ExpectedCues...),
	}
}
