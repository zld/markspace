package probe

import (
	"fmt"

	"github.com/abema/go-mp4"
	"github.com/broadcast/mediad/internal/model"
)

// buildSamples assembles model.Sample values on the unified timeline,
// recording backward / duplicate / gap anomalies with their exact ranges.
func buildSamples(sizes []uint32, deltas, compOffsets []model.Rat, offsets []uint64,
	chunkOf []int, timescale uint32, stss *mp4.Stss, t *model.Track,
	errs *model.ErrorSet) []*model.Sample {
	return buildSamplesAt(sizes, deltas, compOffsets, offsets, chunkOf,
		timescale, stss, t, errs, model.Zero())
}

// buildSamplesAt is buildSamples with an explicit first-sample decode offset.
func buildSamplesAt(sizes []uint32, deltas, compOffsets []model.Rat, offsets []uint64,
	chunkOf []int, timescale uint32, stss *mp4.Stss, t *model.Track,
	errs *model.ErrorSet, start model.Rat) []*model.Sample {

	n := len(sizes)
	samples := make([]*model.Sample, n)
	keySet := keyframeSet(stss, n, errs, t)
	zero := model.Zero()

	cursor := start
	var prevEnd *model.Rat
	for i := 0; i < n; i++ {
		if i > 0 {
			// gap = decode[i] - (decode[i-1] + duration[i-1])
			gap := cursor.Sub(*prevEnd)
			switch gap.Cmp(zero) {
			case 0:
				if deltas[i].Cmp(zero) == 0 {
					errs.Add(anomaly(model.KindTimestampDuplicate, t.ID, i, cursor, deltas[i]))
				}
			case -1:
				errs.Add(anomaly(model.KindTimestampBackward, t.ID, i, cursor, deltas[i]))
			default:
				rg, _ := model.NewRange(*prevEnd, cursor)
				errs.Add(model.MediaError{
					Kind: model.KindTimestampGap, TrackID: t.ID, SampleIndex: i,
					Severity: model.SeverityWarning, Ranges: []model.Range{rg},
					Detail: fmt.Sprintf("gap of %s before sample %d", gap, i),
				})
			}
		}
		samples[i] = &model.Sample{
			Index:       i,
			DecodeTime:  cursor,
			Composition: cursor.Add(compOffsets[i]),
			Duration:    deltas[i],
			Size:        sizes[i],
			IsKeyframe:  keySet[i],
			ChunkIndex:  chunkOf[i],
			Offset:      offsets[i],
		}
		e := cursor.Add(deltas[i])
		prevEnd = &e
		cursor = e
	}
	return samples
}

func keyframeSet(stss *mp4.Stss, n int, errs *model.ErrorSet, t *model.Track) map[int]bool {
	out := make(map[int]bool, n)
	if stss == nil {
		// Only video tracks carry a keyframe index; audio never has one and
		// must not be reported as defective.
		if t.Kind == model.KindVideo {
			errs.Add(model.MediaError{
				Kind: model.KindMissingKeyframeIndex, TrackID: t.ID, SampleIndex: -1,
				Severity: model.SeverityWarning,
				Detail:   "stss absent; keyframe claims limited to provable samples",
			})
		}
		return out
	}
	for _, sn := range stss.SampleNumber {
		idx := int(sn) - 1 // stss is 1-based
		if idx < 0 || idx >= n {
			errs.Add(model.MediaError{
				Kind: model.KindSampleOutOfBounds, TrackID: t.ID, SampleIndex: idx,
				Detail: fmt.Sprintf("stss sample number %d outside 1..%d", sn, n),
			})
			continue
		}
		out[idx] = true
	}
	return out
}

func anomaly(kind model.ErrorKind, trackID uint32, idx int, at, delta model.Rat) model.MediaError {
	end := at.Add(delta)
	if end.Cmp(at) < 0 {
		end = at
	}
	rg, _ := model.NewRange(at, end)
	return model.MediaError{
		Kind: kind, TrackID: trackID, SampleIndex: idx,
		Severity: model.SeverityError, Ranges: []model.Range{rg},
		Detail: fmt.Sprintf("%s at sample %d (delta %s)", kind, idx, delta),
	}
}

// analyzeKeyframes computes adjacent keyframe gaps on the rational timeline
// and reports over-limit intervals plus the index hit rate.
func analyzeKeyframes(t *model.Track, cfg ProbeConfig, errs *model.ErrorSet) {
	if t.Kind != model.KindVideo {
		return
	}
	var keys []*model.Sample
	for _, s := range t.Samples {
		if s.IsKeyframe {
			keys = append(keys, s)
		}
	}
	if !t.KeyframeIndexPresent {
		return // already reported; gaps cannot be proven without an index
	}
	if len(keys) == 0 {
		errs.Add(model.MediaError{
			Kind: model.KindSparseKeyframe, TrackID: t.ID, SampleIndex: -1,
			Detail: "stss present but contains no keyframe entry",
		})
		return
	}
	for i := 1; i < len(keys); i++ {
		gap := keys[i].DecodeTime.Sub(keys[i-1].DecodeTime)
		if gap.Cmp(cfg.MaxKeyframeGap) > 0 {
			rg, _ := model.NewRange(keys[i-1].DecodeTime, keys[i].DecodeTime)
			errs.Add(model.MediaError{
				Kind: model.KindSparseKeyframe, TrackID: t.ID,
				SampleIndex: keys[i].Index, Severity: model.SeverityWarning,
				Ranges: []model.Range{rg},
				Detail: fmt.Sprintf("keyframe gap %s exceeds limit %s", gap, cfg.MaxKeyframeGap),
			})
		}
	}
}
