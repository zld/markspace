package parse

import (
	"bytes"

	"mediaforensics/internal/clock"
	"mediaforensics/internal/fixture"
	"mediaforensics/internal/model"
)

// Result bundles the validated box tree, per-track indexes and every
// non-fatal error collected while parsing. Fatal structural errors are
// returned separately and prevent timeline construction.
type Result struct {
	Container *Container
	Indexes   []*TrackIndex
	Timeline  *model.Timeline
	Errors    []*model.ForensicError
}

// Parse is the single entry point used by the pipeline. It walks the box
// structure, rebuilds missing indexes, converts indexes into model tracks
// and schedules one full-length clip per track. Fatal box errors stop the
// parse; table-level errors are retained and propagated to later stages.
func Parse(data []byte) (*Result, error) {
	r := bytes.NewReader(data)
	container, walkErrs, err := Walk(r, int64(len(data)))
	res := &Result{Container: container, Errors: append([]*model.ForensicError{}, walkErrs...)}
	if err != nil {
		res.Errors = append(res.Errors, model.AsForensicError(err))
		return res, err
	}
	indexes, idxErrs := ExtractIndexes(data, container)
	res.Indexes = indexes
	res.Errors = append(res.Errors, idxErrs...)

	tracks := make([]*model.Track, 0, len(indexes))
	for _, idx := range indexes {
		tracks = append(tracks, indexToTrack(data, idx))
	}
	res.Timeline = model.Build(tracks)
	res.Errors = append(res.Errors, res.Timeline.Errors...)
	return res, nil
}

func indexToTrack(data []byte, idx *TrackIndex) *model.Track {
	tr := &model.Track{
		ID:             idx.TrackID,
		Kind:           idx.Kind,
		Timescale:      idx.Timescale,
		EarliestOffset: idx.EarliestOffset,
	}
	for i := 0; i < idx.SampleCount; i++ {
		var off, size int64 = -1, 0
		if i < len(idx.SampleOffset) {
			off = idx.SampleOffset[i]
		}
		if i < len(idx.SampleSize) {
			size = idx.SampleSize[i]
		}
		confidence, basis := confidenceFor(idx, i)
		corrupt := false
		if off >= 0 && off+size <= int64(len(data)) {
			corrupt = !fixture.ValidPayload(data[off:off+size], idx.TrackID, i)
		} else {
			confidence = model.ConfidenceNone
			basis = "offset outside input boundary"
		}
		keyframe := i < len(idx.Keyframes) && idx.Keyframes[i]
		tr.Samples = append(tr.Samples, model.Sample{
			TrackID:    idx.TrackID,
			Index:      i,
			Offset:     off,
			Size:       size,
			Start:      clock.New(idx.Delta*int64(i), 1),
			Duration:   clock.New(idx.Delta, 1),
			Keyframe:   keyframe,
			Corrupt:    corrupt,
			Confidence: confidence,
			Basis:      basis,
		})
	}
	if len(tr.Samples) > 0 {
		tr.Clips = []model.Clip{{
			TrackID:    idx.TrackID,
			FromSample: 0,
			ToSample:   len(tr.Samples),
		}}
	}
	return tr
}

func confidenceFor(idx *TrackIndex, sample int) (string, string) {
	for _, iv := range idx.Intervals {
		if sample >= iv.FirstSample && sample <= iv.LastSample {
			return iv.Confidence, iv.Basis
		}
	}
	if idx.Rebuilt {
		return model.ConfidenceLow, "outside rebuilt intervals"
	}
	return model.ConfidenceHigh, "complete stco/stsc/stsz/stts"
}
