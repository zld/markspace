package media

// ValidateMovieBounds checks every sample against the file extent and the
// mdat ranges. Offsets outside an mdat payload are treated as invalid samples.
func ValidateMovieBounds(p *Probe) error {
	if p.Movie == nil || len(p.Movie.Mdat) == 0 {
		return NewError(ClassUnsupported, "bounds", 0, "no mdat box found")
	}
	for _, ti := range p.Tracks {
		st := ti.Table
		for i := range st.Samples {
			s := st.Samples[i]
			end := s.Offset + uint64(s.Size)
			if end < s.Offset || end > uint64(p.Size) {
				return NewError(ClassInvalidSample, "bounds", int64(s.Offset),
					"sample %d overflows file (offset=%d size=%d)", s.Index, s.Offset, s.Size).WithTrack(st.TrackID)
			}
			if !insideMdat(p.Movie.Mdat, s.Offset, end) {
				return NewError(ClassInvalidSample, "bounds", int64(s.Offset),
					"sample %d outside mdat", s.Index).WithTrack(st.TrackID)
			}
		}
	}
	return nil
}

func insideMdat(mdats []Box, start, end uint64) bool {
	for _, m := range mdats {
		lo := uint64(m.Offset + m.Header)
		hi := uint64(m.Offset + m.Size)
		if start >= lo && end <= hi {
			return true
		}
	}
	return false
}

// SplitSegments partitions tracks into segments of at most samplesPer samples.
// Segment indices are deterministic: target track order, then sample order.
func SplitSegments(tracks []*TrackInfo, samplesPer int) ([]Segment, error) {
	if samplesPer <= 0 {
		return nil, NewError(ClassInvalidArgument, "split", 0, "samples per segment must be positive")
	}
	var segs []Segment
	for _, ti := range tracks {
		st := ti.Table
		if st == nil {
			return nil, NewError(ClassInvalidArgument, "split", 0, "track %d has no table", ti.TrackID)
		}
		for first, segIdx := uint32(0), 0; first < st.Count; first, segIdx = first+uint32(samplesPer), segIdx+1 {
			count := uint32(samplesPer)
			if first+count > st.Count {
				count = st.Count - first
			}
			var sum uint64
			var bytes int64
			for i := uint32(0); i < count; i++ {
				s := st.Samples[first+i]
				sum += uint64(s.Dur)
				bytes += int64(s.Size)
			}
			segs = append(segs, Segment{
				Index: len(segs), TrackID: st.TrackID, TrackSeg: segIdx,
				First: first, Count: count, DTSStart: st.Samples[first].DTS,
				Duration: sum, ByteSize: bytes,
			})
		}
	}
	return segs, nil
}
