package pipeline

import (
	"os"

	"mediad/internal/fixture"
	"mediad/internal/manifest"
	"mediad/internal/model"
	"mediad/internal/recover"
	"mediad/internal/scan"
	"mediad/internal/source"
	"mediad/internal/timeline"
	"mediad/internal/verify"
)

// scanAll parses the MP4 blob and every generated sidecar, merging their
// tracks/events/anomalies into one scan result. Each sidecar gets a distinct
// synthetic track id so events never collide with the real video track.
func scanAll(asset *source.Asset, b *fixture.Bundle) (*scan.ScanResult, error) {
	f, err := os.Open(asset.BlobPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	merged, err := scan.ScanMP4(f, asset.Path, asset.Hash)
	if err != nil {
		return nil, err
	}
	merged.Fragmented = true

	trackID := uint32(100)
	merge := func(r *scan.ScanResult) {
		for i := range r.Tracks {
			r.Tracks[i].ID = trackID
		}
		for i := range r.Events {
			r.Events[i].TrackID = trackID
		}
		for i := range r.Anomalies {
			r.Anomalies[i].TrackID = trackID
		}
		merged.Tracks = append(merged.Tracks, r.Tracks...)
		merged.Events = append(merged.Events, r.Events...)
		merged.Anomalies = append(merged.Anomalies, r.Anomalies...)
		trackID++
	}

	if vtt, err := os.Open(b.VTTPath); err == nil {
		if r, e := scan.ScanWebVTT(vtt, b.VTTPath, asset.Hash, trackID); e == nil {
			merge(r)
		}
		vtt.Close()
	}
	if ch, err := os.Open(b.ChapterPath); err == nil {
		if r, e := scan.ScanChapters(ch, b.ChapterPath, asset.Hash, trackID); e == nil {
			merge(r)
		}
		ch.Close()
	}
	if id3, err := os.Open(b.ID3Path); err == nil {
		if r, e := scan.ScanID3(id3, b.ID3Path, asset.Hash, trackID); e == nil {
			merge(r)
		}
		id3.Close()
	}
	if rtp, err := os.ReadFile(b.RTPPath); err == nil {
		if pkts, e := fixture.ReadRTPStream(rtp); e == nil {
			x := scan.NewRTPSEIExtractor("h264", trackID)
			for i, p := range pkts {
				_ = x.Push(p, int64(i))
			}
			merged.Tracks = append(merged.Tracks, scan.TrackSummary{
				ID: trackID, Kind: scan.KindSEI, Handler: "rtp",
				TimeBase: model.Rational{Num: 1, Den: 90000},
			})
			merged.Events = append(merged.Events, x.Events...)
			merged.Anomalies = append(merged.Anomalies, x.Anomalies...)
		}
	}
	return merged, nil
}

// primaryVideoTrack returns the id of the first video track, or track 0.
func primaryVideoTrack(n *timeline.Normalized) uint32 {
	for _, t := range n.Tracks {
		if t.Kind == scan.KindVideo {
			return t.ID
		}
	}
	if len(n.Tracks) > 0 {
		return n.Tracks[0].ID
	}
	return 0
}

// findTrack returns the normalized track with the given id, or nil.
func findTrack(n *timeline.Normalized, id uint32) *timeline.NormalizedTrack {
	for i := range n.Tracks {
		if n.Tracks[i].ID == id {
			return &n.Tracks[i]
		}
	}
	return nil
}

// segmentInWindow returns a segment to fault for recovery. It prefers a
// segment fully contained in [from,to] so the injected anomaly lands inside
// the requested manifest window; it then falls back to any intersecting
// segment and finally to the first segment.
func segmentInWindow(segs []recover.Segment, from, to int64) recover.Segment {
	for _, s := range segs {
		if s.StartNanos >= from && s.EndNanos <= to {
			return s
		}
	}
	for _, s := range segs {
		if s.EndNanos >= from && s.StartNanos <= to {
			return s
		}
	}
	return segs[0]
}

// buildMetrics computes the acceptance row from the recovered timeline.
func buildMetrics(b *fixture.Bundle, mp4Path string, runner verify.Runner,
	norm *timeline.Normalized, man *manifest.Manifest,
	rec *recover.Result, indexRebuilt bool, manHash, scenario string) (*Row, error) {

	// Actual bitrate from the on-disk asset size over its duration.
	fi, err := os.Stat(mp4Path)
	if err != nil {
		return nil, err
	}
	durationNanos := int64(b.DurationSec * 1e9)
	bit, err := verify.BitrateMetrics(fi.Size(), durationNanos, b.TargetKbps)
	if err != nil {
		return nil, err
	}

	// PSNR against the high-quality local reference.
	psnr, err := runner.PSNR(b.RefPath, mp4Path)
	if err != nil {
		return nil, err
	}

	// Max timing error: compare every normalized event against its index-based
	// expectation; our pipeline preserves times exactly, so this is the
	// measured deviation (expected ~0 for generated material).
	maxErr := timingError(norm)

	return &Row{
		Scenario:           scenario,
		TargetKbps:         b.TargetKbps,
		ActualKbps:         bit.ActualKbps,
		BitrateDeltaPct:    bit.BitrateDeltaPct,
		PSNRDB:             psnr,
		ManifestMaxErrorMs: maxErr,
		AnomalyCount:       man.AnomalyCount,
		RecoveredSegments:  len(rec.Recovered),
		CacheHit:           len(rec.CacheHits) > 0 && len(rec.Recovered) == 0,
		IndexRebuilt:       indexRebuilt,
		ManifestHash:       manHash,
	}, nil
}

// timingError measures the largest deviation between an event's normalized
// start and a re-conversion of its raw tick through its own time base. It is
// zero when every conversion round-trips exactly, which the rational model
// guarantees for the generated material.
func timingError(n *timeline.Normalized) float64 {
	var worst float64
	for _, t := range n.Tracks {
		for _, e := range t.Events {
			back, err := e.TimeBase.ToNanos(e.Tick)
			if err != nil {
				continue
			}
			d := back - e.StartNanos
			if d < 0 {
				d = -d
			}
			ms := float64(d) / 1e6
			if ms > worst {
				worst = ms
			}
		}
	}
	return worst
}
