package adapter

import (
	"mediacore/media"
)

// applyEditList maps track-media time onto the movie presentation axis.
// Empty edits (media_time == -1) become explicit gaps; media times outside
// the sample table are rejected as corrupt edit segments. Presentation
// timing is preserved and the original non-zero start PTS is retained.
func applyEditList(st *mp4TrackState, tr *TrackManifest, movieScale media.Rational) error {
	if st.elst == nil || len(st.elst.Entries) == 0 {
		tr.StartPTS = 0
		return nil
	}
	scale := media.TimeScale(st.timescale)
	var movieCursor uint64
	edits := make([]Edit, 0, len(st.elst.Entries))
	presented := make([]Sample, 0, len(tr.Samples))
	for idx, e := range st.elst.Entries {
		segMovie := int64(e.SegmentDurationV0)
		if segMovie <= 0 {
			return media.Errorf(media.CodeBadEditSegment, st.trackID, nil, st.byteStart,
				"elst entry %d has non-positive segment duration", idx)
		}
		start, err := media.ToNanos(int64(movieCursor), movieScale)
		if err != nil {
			return media.Errorf(media.CodeOverflow, st.trackID, nil, st.byteStart, "elst: %v", err)
		}
		segNanos, err := media.ToNanos(segMovie, movieScale)
		if err != nil {
			return media.Errorf(media.CodeOverflow, st.trackID, nil, st.byteStart, "elst: %v", err)
		}
		iv := media.Interval{Start: start, End: start + segNanos}
		if e.MediaTimeV0 < 0 {
			// empty edit: a presentation gap, contributes no samples
			edits = append(edits, Edit{TrackID: st.trackID, Segment: iv, MediaTime: -1})
			movieCursor += uint64(segMovie)
			continue
		}
		mediaStart, err := media.ToNanos(int64(e.MediaTimeV0), scale)
		if err != nil {
			return media.Errorf(media.CodeOverflow, st.trackID, nil, st.byteStart, "elst: %v", err)
		}
		mediaEnd := mediaStart + segNanos
		lastPTS := int64(0)
		if n := len(tr.Samples); n > 0 {
			lastPTS = tr.Samples[n-1].PTS
		}
		if len(tr.Samples) == 0 || mediaStart > lastPTS {
			return media.Errorf(media.CodeBadEditSegment, st.trackID, &iv, st.byteStart,
				"elst entry %d media time %d exceeds track duration", idx, e.MediaTimeV0)
		}
		edits = append(edits, Edit{TrackID: st.trackID, Segment: iv, MediaTime: int64(e.MediaTimeV0)})
		if tr.StartPTS == 0 && len(presented) == 0 {
			tr.StartPTS = mediaStart
		}
		// Only samples inside this edit's media window are presented. They
		// are shifted so mediaStart lands on the segment's movie start.
		offset := start - mediaStart
		for _, s := range tr.Samples {
			if s.PTS >= mediaStart && s.PTS < mediaEnd {
				s.PTS += offset
				presented = append(presented, s)
			}
		}
		movieCursor += uint64(segMovie)
	}
	tr.Samples = presented
	tr.Edits = edits
	return nil
}

func (p *mp4Parser) buildManifest() (*Manifest, error) {
	if len(p.tracks) == 0 {
		return nil, media.NewError(media.CodeMalformed, 0, nil, -1, "moov contains no tracks")
	}
	m := &Manifest{Source: "mp4", AdapterName: MP4AdapterName, Version: MP4AdapterV1}
	movieScale := media.TimeScale(p.movieScale)
	if movieScale.Num == 0 {
		movieScale = media.TimeScale(1000)
	}
	for _, st := range p.tracks {
		if st.timescale == 0 {
			return nil, media.NewError(media.CodeMalformed, st.trackID, nil,
				st.byteStart, "mdhd timescale is zero")
		}
		samples, dur, err := p.expandTables(st)
		if err != nil {
			return nil, err
		}
		kind := KindAudio
		if st.video {
			kind = KindVideo
		}
		tr := &TrackManifest{
			TrackID:   st.trackID,
			Kind:      kind,
			Timescale: media.TimeScale(st.timescale),
			Duration:  dur,
			Samples:   samples,
		}
		if err := applyEditList(st, tr, movieScale); err != nil {
			return nil, err
		}
		m.Tracks = append(m.Tracks, tr)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}
