package media

import "fmt"

// SampleTable is the fully expanded, validated sample table of one track.
// All times are in the track's media timescale.
type SampleTable struct {
	TrackID   uint32
	Timescale uint32
	Count     uint32
	Offsets   []uint64
	Sizes     []uint32
	Durations []uint32
	CTS       []int64 // composition time offsets relative to DTS
	DTS       []uint64
	Sync      []bool
	Samples   []SampleRef
}

// SampleRef locates one compressed sample in the source file.
type SampleRef struct {
	TrackID uint32
	Index   uint32 // 0-based
	Offset  uint64
	Size    uint32
	Dur     uint32
	DTS     uint64
	CTS     int64 // DTS + composition offset
	Sync    bool
}

// Segment is a contiguous run of samples from one track. Global Index is
// assigned deterministically (track order, then segment order).
type Segment struct {
	Index    int    `json:"index"`
	TrackID  uint32 `json:"track_id"`
	TrackSeg int    `json:"track_segment"`
	First    uint32 `json:"first_sample"` // 0-based, inclusive
	Count    uint32 `json:"sample_count"`
	DTSStart uint64 `json:"dts_start"`
	Duration uint64 `json:"duration"`
	ByteSize int64  `json:"byte_size"`
	SHA256   string `json:"sha256,omitempty"`
}

// LastIndex returns the 0-based index of the final sample in the segment.
func (s Segment) LastIndex() uint32 { return s.First + s.Count - 1 }

func (s Segment) String() string {
	return fmt.Sprintf("segment %d track=%d samples=%d..%d", s.Index, s.TrackID, s.First, s.LastIndex())
}

// Ref returns the per-sample reference at position i within the segment.
func (s Segment) Ref(st *SampleTable, i uint32) (SampleRef, error) {
	idx := s.First + i
	if i >= s.Count || idx >= st.Count {
		return SampleRef{}, NewError(ClassInvalidSample, "segment.ref", 0, "sample %d outside segment", idx).WithTrack(st.TrackID)
	}
	return st.Samples[idx], nil
}

// buildSampleTable expands stts/ctts/stsc/stsz/stco(st)/stss into per-sample rows.
func buildSampleTable(rt *rawTrack) (*SampleTable, error) {
	id := rt.Tkhd.TrackID
	if rt.Stsz.SampleCount == 0 {
		return nil, NewError(ClassInvalidSample, "sample.size", rt.StszBox.Offset, "no samples")
	}
	count := rt.Stsz.SampleCount
	st := &SampleTable{TrackID: id, Timescale: rt.Mdhd.Timescale, Count: count}

	var rows uint32
	for _, e := range rt.Stts.Entries {
		if e.SampleCount == 0 {
			return nil, NewError(ClassInvalidSample, "sample.stts", rt.SttsBox.Offset, "zero-count stts entry")
		}
		rows += e.SampleCount
		if rows < e.SampleCount {
			return nil, NewError(ClassSizeOverflow, "sample.stts", rt.SttsBox.Offset, "stts sample count overflow")
		}
	}
	if rows != count {
		return nil, NewError(ClassInvalidSample, "sample.stts", rt.SttsBox.Offset, "stts covers %d samples, stsz declares %d", rows, count)
	}
	st.Durations = make([]uint32, 0, count)
	for _, e := range rt.Stts.Entries {
		for i := uint32(0); i < e.SampleCount; i++ {
			st.Durations = append(st.Durations, e.SampleDelta)
		}
	}
	st.DTS = make([]uint64, count)
	var acc uint64
	for i := range st.DTS {
		st.DTS[i] = acc
		next := acc + uint64(st.Durations[i])
		if next < acc {
			return nil, NewError(ClassTimebase, "sample.stts", rt.SttsBox.Offset, "decode time overflow")
		}
		acc = next
	}

	st.CTS = make([]int64, count)
	if rt.Ctts != nil {
		var ci int
		for i := range rt.Ctts.Entries {
			e := rt.Ctts.Entries[i]
			off := rt.Ctts.GetSampleOffset(i)
			for k := uint32(0); k < e.SampleCount; k++ {
				if ci >= int(count) {
					return nil, NewError(ClassInvalidSample, "sample.ctts", rt.CttsBox.Offset, "ctts covers more samples than stsz")
				}
				st.CTS[ci] = off
				ci++
			}
		}
		if ci != int(count) {
			return nil, NewError(ClassInvalidSample, "sample.ctts", rt.CttsBox.Offset, "ctts covers %d of %d samples", ci, count)
		}
	}

	st.Sizes = make([]uint32, count)
	if rt.Stsz.SampleSize != 0 {
		for i := range st.Sizes {
			st.Sizes[i] = rt.Stsz.SampleSize
		}
	} else {
		if uint32(len(rt.Stsz.EntrySize)) != count {
			return nil, NewError(ClassInvalidSample, "sample.stsz", rt.StszBox.Offset, "stsz entries %d != sample count %d", len(rt.Stsz.EntrySize), count)
		}
		copy(st.Sizes, rt.Stsz.EntrySize)
	}

	var chunkOffsets []uint64
	if rt.Stco != nil {
		if uint32(len(rt.Stco.ChunkOffset)) != rt.Stco.EntryCount {
			return nil, NewError(ClassInvalidSample, "sample.stco", rt.StcoBox.Offset, "stco entry count mismatch")
		}
		for _, o := range rt.Stco.ChunkOffset {
			chunkOffsets = append(chunkOffsets, uint64(o))
		}
	} else if rt.Co64 != nil {
		chunkOffsets = append(chunkOffsets, rt.Co64.ChunkOffset...)
	}
	if len(chunkOffsets) == 0 {
		return nil, NewError(ClassInvalidSample, "sample.chunk", rt.Trak.Offset, "no chunk offsets")
	}

	spc := make([]uint32, len(chunkOffsets))
	var prevFirst uint32
	for i, e := range rt.Stsc.Entries {
		if e.FirstChunk == 0 {
			return nil, NewError(ClassInvalidSample, "sample.stsc", rt.StscBox.Offset, "first_chunk is 1-based and may not be 0")
		}
		if i > 0 && e.FirstChunk <= prevFirst {
			return nil, NewError(ClassInvalidSample, "sample.stsc", rt.StscBox.Offset, "first_chunk values are not increasing")
		}
		prevFirst = e.FirstChunk
		start := int(e.FirstChunk) - 1
		end := len(chunkOffsets)
		if i+1 < len(rt.Stsc.Entries) {
			nxt := int(rt.Stsc.Entries[i+1].FirstChunk) - 1
			if nxt < start {
				return nil, NewError(ClassInvalidSample, "sample.stsc", rt.StscBox.Offset, "overlapping chunk runs")
			}
			if nxt < end {
				end = nxt
			}
		}
		if start > len(chunkOffsets) {
			return nil, NewError(ClassInvalidSample, "sample.stsc", rt.StscBox.Offset, "first_chunk %d beyond chunk count %d", e.FirstChunk, len(chunkOffsets))
		}
		for c := start; c < end; c++ {
			spc[c] = e.SamplesPerChunk
		}
	}

	st.Offsets = make([]uint64, 0, count)
	st.Samples = make([]SampleRef, 0, count)
	var sample uint32
	for c, off := range chunkOffsets {
		for k := uint32(0); k < spc[c]; k++ {
			if sample >= count {
				return nil, NewError(ClassInvalidSample, "sample.chunk", int64(off), "chunks describe more samples than stsz")
			}
			st.Offsets = append(st.Offsets, off)
			st.Samples = append(st.Samples, SampleRef{
				TrackID: id, Index: sample, Offset: off, Size: st.Sizes[sample],
				Dur: st.Durations[sample], DTS: st.DTS[sample],
				CTS: int64(st.DTS[sample]) + st.CTS[sample],
			})
			off += uint64(st.Sizes[sample])
			sample++
		}
	}
	if sample != count {
		return nil, NewError(ClassInvalidSample, "sample.chunk", 0, "chunks cover %d of %d samples", sample, count)
	}

	st.Sync = make([]bool, count)
	if rt.Stss == nil {
		for i := range st.Sync {
			st.Sync[i] = true
		}
	} else {
		if uint32(len(rt.Stss.SampleNumber)) != rt.Stss.EntryCount {
			return nil, NewError(ClassInvalidSample, "sample.stss", rt.StssBox.Offset, "stss entry count mismatch")
		}
		for _, n := range rt.Stss.SampleNumber {
			if n == 0 || n > count {
				return nil, NewError(ClassInvalidSample, "sample.stss", rt.StssBox.Offset, "sync sample number %d out of range 1..%d", n, count)
			}
			if st.Sync[n-1] {
				return nil, NewError(ClassInvalidSample, "sample.stss", rt.StssBox.Offset, "duplicate sync sample number %d", n)
			}
			st.Sync[n-1] = true
		}
	}
	for i := range st.Samples {
		st.Samples[i].Sync = st.Sync[i]
	}
	return st, nil
}

// validateSamples performs self-consistency checks that need no file access.
func validateSamples(st *SampleTable) error {
	if st.Timescale == 0 {
		return NewError(ClassTimebase, "sample.validate", 0, "zero timescale").WithTrack(st.TrackID)
	}
	if uint32(len(st.Samples)) != st.Count {
		return NewError(ClassInvalidSample, "sample.validate", 0, "row count %d != declared %d", len(st.Samples), st.Count).WithTrack(st.TrackID)
	}
	var prev uint64
	for i := range st.Samples {
		s := &st.Samples[i]
		if i > 0 && s.DTS < prev {
			return NewError(ClassInvalidSample, "sample.validate", int64(s.Offset), "decode time went backwards").WithTrack(st.TrackID)
		}
		prev = s.DTS
		if s.Size == 0 {
			return NewError(ClassInvalidSample, "sample.validate", int64(s.Offset), "sample %d has zero size", s.Index).WithTrack(st.TrackID)
		}
		if s.CTS < 0 {
			return NewError(ClassInvalidSample, "sample.validate", int64(s.Offset), "sample %d has negative composition time", s.Index).WithTrack(st.TrackID)
		}
	}
	if !st.Sync[0] {
		return NewError(ClassInvalidSample, "sample.validate", 0, "first sample is not a sync sample").WithTrack(st.TrackID)
	}
	return nil
}
