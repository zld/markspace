package pipeline

import (
	"path/filepath"

	"mediacore/internal/clock"
	"mediacore/internal/index"
	"mediacore/internal/mp4"
)

func (rn *Runner) runParse() error {
	manifest, err := mp4.LoadManifest(rn.ManifestPath)
	if err != nil {
		return err
	}
	parsed, err := mp4.ParseManifest(manifest)
	if err != nil {
		return err
	}
	a := &analysis{manifest: manifest, parsed: parsed, indexes: map[string]*index.Index{}}
	var indexes []*index.Index
	for _, ps := range parsed {
		if ps.Source.Kind != mp4.SourceMP4 || ps.MP4 == nil {
			continue
		}
		fileSize := int64(0)
		if ps.File != nil {
			fileSize = ps.File.Size()
		}
		idx, err := index.Build(ps.Source.ID, fileSize, ps.MP4)
		if err != nil {
			return err
		}
		a.indexes[ps.Source.ID] = idx
		indexes = append(indexes, idx)
		if ps.Source.Role == "video" || a.primary == nil {
			a.primary = idx
		}
	}
	if a.primary == nil {
		return clock.New(clock.CategoryMissingTrack, rn.ManifestPath, 0, 0, "manifest has no queryable MP4 track")
	}
	if err := index.CheckTimebases(indexes); err != nil {
		return err
	}
	a.indexList = indexes
	rn.Report.TrackCount = len(indexes)
	rn.Report.ParseRoundtripRate = roundtripRate(parsed, a.indexes)
	rn.Report.KeyframeHitRate = keyframeHitRate(a.primary)
	rn.data = a

	payload := map[string]any{
		"batch_id": rn.BatchID, "source_count": len(parsed), "index_count": len(a.indexes),
		"parse_roundtrip_rate": rn.Report.ParseRoundtripRate,
		"keyframe_hit_rate":    rn.Report.KeyframeHitRate,
	}
	return rn.materializeCommitted("parse", filepath.Join(rn.WorkDir, "artifacts", "parse.json"), payload)
}

func roundtripRate(parsed []mp4.ParsedSource, indexes map[string]*index.Index) float64 {
	total, pass := 0, 0
	for _, ps := range parsed {
		switch {
		case ps.MP4 != nil:
			idx := indexes[ps.Source.ID]
			if idx == nil || idx.TimeMap == nil {
				continue
			}
			for _, s := range idx.Samples {
				total++
				tickErr, err := idx.TimeMap.RoundTripError(clock.MediaTime(s.PTS))
				if err == nil && tickErr <= 1 {
					pass++
				}
			}
		case ps.RTP != nil:
			pkts := ps.RTP.Packets
			if len(pkts) == 0 {
				continue
			}
			first := clock.MediaTime(pkts[0].UnwrappedTimestamp)
			last := pkts[len(pkts)-1].UnwrappedTimestamp
			dur := last - int64(first) + int64(ps.RTP.TimeScale)
			tm, err := clock.NewIdentityMap(ps.RTP.Path, ps.RTP.Number, ps.RTP.TimeScale, first, dur)
			if err != nil {
				continue
			}
			for _, p := range pkts {
				total++
				tickErr, err := tm.RoundTripError(clock.MediaTime(p.UnwrappedTimestamp))
				if err == nil && tickErr <= 1 {
					pass++
				}
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(pass) / float64(total)
}

func keyframeHitRate(idx *index.Index) float64 {
	if idx == nil || len(idx.Keyframes) == 0 {
		return 0
	}
	hits := 0
	for _, k := range idx.Keyframes {
		res, err := idx.Query(index.Range{Start: k.EventStart, End: k.EventStart + 1})
		if err == nil && res.Keyframe != nil && res.Keyframe.SampleNumber == k.SampleNumber {
			hits++
		}
	}
	return float64(hits) / float64(len(idx.Keyframes))
}
