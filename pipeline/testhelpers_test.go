package pipeline

import (
	"mediamux/analyze"
	"mediamux/manifest"
	"mediamux/media"
)

func analyzeSample(durMS int64) analyze.QualitySample {
	return analyze.QualitySample{DurationMS: durMS}
}

func manifestDoc(n int) *manifest.Document {
	d := &manifest.Document{Kind: manifest.KindHLS, Timescale: manifest.OutputTimescale}
	for i := 0; i < n; i++ {
		d.Segments = append(d.Segments, manifest.Segment{
			Index:    i,
			ID:       "s",
			Start:    media.NewTime(int64(i)*6000*90, manifest.OutputTimescale),
			Duration: media.NewTime(6000*90, manifest.OutputTimescale),
		})
	}
	return d
}
