package segment

import (
	"fmt"

	mediahash "github.com/zld/mediarc/internal/hash"
	"github.com/zld/mediarc/internal/model"
	"github.com/zld/mediarc/internal/timeline"
)

// PayloadFunc returns the deterministic media bytes for one sample.
type PayloadFunc func(trackID, sampleNumber uint32) []byte

// Cut selects shared reference boundaries and corresponding keyframes on
// every active representation.
func Cut(built *timeline.Built, tracks []model.TrackMetadata, cfg model.Config, payload PayloadFunc) (*Report, error) {
	if built == nil || built.Index == nil {
		return nil, &model.ConfigError{Field: "timeline", Err: fmt.Errorf("missing built timeline")}
	}
	c := cutContext{built: built, index: built.Index, cfg: cfg, tracks: tracks, payload: payload}
	active, trackReports, issues := c.activeTracks()
	refID := cfg.ReferenceTrackID
	if refID == 0 && len(active) > 0 {
		refID = active[0].TrackID
	}
	var ref *model.TrackMetadata
	for i := range active {
		if active[i].TrackID == refID {
			ref = &active[i]
			break
		}
	}
	if ref == nil {
		return nil, &model.AlignmentError{TrackID: refID, Boundary: "reference", DeviationNS: -1, ToleranceNS: cfg.BoundaryToleranceNS}
	}

	refBoundaries, partialTail, err := c.referenceBoundaries(refID)
	if err != nil {
		return nil, err
	}

	report := &Report{ReferenceTrackID: refID, Tracks: trackReports, Issues: issues}
	for i := 0; i+1 < len(refBoundaries); i++ {
		seg, checks, hits, err := c.cutInterval(i, refBoundaries[i], refBoundaries[i+1], i == len(refBoundaries)-2 && partialTail, active)
		if err != nil {
			return nil, err
		}
		report.Segments = append(report.Segments, seg)
		report.KeyframeChecks += checks
		report.KeyframeHits += hits
	}
	return report, nil
}

func (c cutContext) activeTracks() ([]model.TrackMetadata, []TrackReport, []model.Issue) {
	var active []model.TrackMetadata
	var reports []TrackReport
	var issues []model.Issue
	if len(c.tracks) > 0 {
		for _, tr := range c.tracks {
			rep := TrackReport{TrackID: tr.TrackID, Representation: tr.Representation, BitrateKbps: tr.BitrateKbps, Empty: tr.Empty || tr.EventCount == 0}
			if rep.Empty {
				rep.Included = false
				rep.Reason = "empty_track"
				issues = append(issues, model.Issue{
					Code: "empty_track", TrackID: tr.TrackID, Message: "empty track explicitly excluded from representations",
				})
			} else {
				rep.Included = true
				active = append(active, tr)
			}
			reports = append(reports, rep)
		}
		return active, reports, issues
	}
	for _, kf := range c.index.All() {
		if trackMetadataExists(active, kf.TrackID) {
			continue
		}
		tr := model.TrackMetadata{TrackID: kf.TrackID, Representation: kf.Representation}
		active = append(active, tr)
		reports = append(reports, TrackReport{TrackID: kf.TrackID, Representation: kf.Representation, Included: true})
	}
	return active, reports, issues
}

func trackMetadataExists(tracks []model.TrackMetadata, id uint32) bool {
	for _, tr := range tracks {
		if tr.TrackID == id {
			return true
		}
	}
	return false
}

func (c cutContext) referenceBoundaries(refID uint32) ([]Boundary, bool, error) {
	frames := c.index.Track(refID)
	if len(frames) < 2 {
		return nil, false, &model.AlignmentError{TrackID: refID, Representation: "reference", Boundary: "keyframe-index", DeviationNS: 0, ToleranceNS: c.cfg.BoundaryToleranceNS}
	}
	first := frames[0]
	last := frames[len(frames)-1]
	span := last.TimeNS - first.TimeNS
	full := int(span / c.cfg.SegmentDurationNS)
	boundaries := []Boundary{boundaryFromKeyframe(first, 0)}
	for k := 1; k <= full; k++ {
		target := first.TimeNS + model.NS(k)*c.cfg.SegmentDurationNS
		kf, dev, ok := c.index.Nearest(refID, target)
		if !ok || kf.SampleNumber == boundaries[len(boundaries)-1].SampleNumber {
			return nil, false, &model.AlignmentError{TrackID: refID, Boundary: fmt.Sprintf("reference-boundary-%d", k), DeviationNS: dev, ToleranceNS: c.cfg.BoundaryToleranceNS}
		}
		if dev.Abs() > c.cfg.BoundaryToleranceNS {
			return nil, false, &model.AlignmentError{TrackID: refID, Boundary: fmt.Sprintf("reference-boundary-%d", k), DeviationNS: dev, ToleranceNS: c.cfg.BoundaryToleranceNS}
		}
		boundaries = append(boundaries, boundaryFromKeyframe(kf, dev))
	}
	partialTail := false
	lastSelected := boundaries[len(boundaries)-1]
	if last.SampleNumber != lastSelected.SampleNumber && last.TimeNS > lastSelected.TimeNS {
		if last.TimeNS-lastSelected.TimeNS < c.cfg.SegmentDurationNS {
			partialTail = true
		}
		boundaries = append(boundaries, boundaryFromKeyframe(last, last.TimeNS-lastSelected.TimeNS))
	}
	if len(boundaries) < 2 {
		return nil, false, &model.AlignmentError{TrackID: refID, Boundary: "segment-count", DeviationNS: span, ToleranceNS: c.cfg.SegmentDurationNS}
	}
	return boundaries, partialTail, nil
}

func (c cutContext) cutInterval(index int, startRef, endRef Boundary, lastPartial bool, tracks []model.TrackMetadata) (Segment, int, int, error) {
	seg := Segment{
		Index:            index,
		StartNS:          startRef.TimeNS,
		EndNS:            endRef.TimeNS,
		DurationNS:       endRef.TimeNS - startRef.TimeNS,
		TargetDurationNS: c.cfg.SegmentDurationNS,
		Partial:          lastPartial,
	}
	if lastPartial {
		seg.Reason = "tail_below_target_duration"
	}
	checks, hits := 0, 0
	canonical := mediahash.New()
	for _, tr := range tracks {
		startKF, startDev, ok := c.index.Nearest(tr.TrackID, startRef.TimeNS)
		if !ok {
			return seg, 0, 0, &model.AlignmentError{Representation: tr.Representation, TrackID: tr.TrackID, Boundary: "start", ToleranceNS: c.cfg.BoundaryToleranceNS}
		}
		endKF, endDev, ok := c.index.Nearest(tr.TrackID, endRef.TimeNS)
		if !ok {
			return seg, 0, 0, &model.AlignmentError{Representation: tr.Representation, TrackID: tr.TrackID, Boundary: "end", ToleranceNS: c.cfg.BoundaryToleranceNS}
		}
		checks += 2
		hits += 2
		if startDev.Abs() > c.cfg.BoundaryToleranceNS {
			return seg, 0, 0, &model.AlignmentError{Representation: tr.Representation, TrackID: tr.TrackID, Boundary: fmt.Sprintf("segment-%d-start", index), DeviationNS: startDev, ToleranceNS: c.cfg.BoundaryToleranceNS}
		}
		if endDev.Abs() > c.cfg.BoundaryToleranceNS {
			return seg, 0, 0, &model.AlignmentError{Representation: tr.Representation, TrackID: tr.TrackID, Boundary: fmt.Sprintf("segment-%d-end", index), DeviationNS: endDev, ToleranceNS: c.cfg.BoundaryToleranceNS}
		}
		endSampleExclusive := endKF.SampleNumber
		if lastPartial {
			endSampleExclusive = endKF.SampleNumber + 1
		}
		rs := RepSegment{
			TrackID:        tr.TrackID,
			Representation: tr.Representation,
			BitrateKbps:    tr.BitrateKbps,
			Start:          boundaryFromKeyframe(startKF, startDev),
			End:            boundaryFromKeyframe(endKF, endDev),
			StartSample:    startKF.SampleNumber,
			EndSample:      endSampleExclusive,
		}
		h := mediahash.New()
		var chunkBytes []byte
		for _, ev := range c.built.Continuous {
			if ev.Raw.TrackID != tr.TrackID {
				continue
			}
			order := ev.Raw.OrderIndex
			if order < startKF.OrderIndex {
				continue
			}
			if lastPartial {
				if order > endKF.OrderIndex {
					continue
				}
			} else if order >= endKF.OrderIndex {
				continue
			}
			payload := c.payloadFor(ev)
			rs.ChunkCount++
			rs.ByteCount += len(payload)
			chunkBytes = append(chunkBytes, payload...)
			_, _ = h.Write(payload)
		}
		if rs.ChunkCount == 0 {
			return seg, 0, 0, &model.ParseError{
				TrackID: int(tr.TrackID), BoxType: "sample", ByteOffset: startKF.Offset,
				Err: fmt.Errorf("no samples between keyframes %d and %d", startKF.SampleNumber, endKF.SampleNumber),
			}
		}
		rs.ChunkBytes = chunkBytes
		rs.SHA256 = fmt.Sprintf("%x", h.Sum(nil))
		seg.Representations = append(seg.Representations, rs)
		_, _ = fmt.Fprintf(canonical, "%d|%d|%d|%s\n", tr.TrackID, rs.StartSample, rs.EndSample, rs.SHA256)
	}
	seg.SHA256 = fmt.Sprintf("%x", canonical.Sum(nil))
	return seg, checks, hits, nil
}

func (c cutContext) event(trackID, sampleNumber uint32) (timeline.Event, bool) {
	for _, ev := range c.built.Continuous {
		if ev.Raw.TrackID == trackID && ev.Raw.SampleNumber == sampleNumber {
			return ev, true
		}
	}
	return timeline.Event{}, false
}

func (c cutContext) payloadFor(ev timeline.Event) []byte {
	if c.payload != nil {
		if p := c.payload(ev.Raw.TrackID, ev.Raw.SampleNumber); p != nil {
			return p
		}
	}
	return ev.Raw.Payload
}
