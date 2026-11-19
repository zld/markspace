// Package index builds the per-track sample and keyframe index from the
// parsed movie model. Only sample tables are read; media payloads in
// mdat are never touched while indexing.
package index

import (
	"fmt"

	"mp4seek/internal/movie"
	"mp4seek/internal/timeline"
)

// Sample is one indexed media sample.
type Sample struct {
	Index  int   // 0-based sample number
	Offset int64 // absolute file offset of the sample payload
	Size   int64 // sample size in bytes
	DTS    int64 // decode time, unwrapped to 64-bit, media timescale units
	CTS    int64 // composition offset (signed), media timescale units
	PTS    int64 // DTS + CTS
	Sync   bool  // keyframe / random access point
}

// TrackIndex is the built index for one track.
type TrackIndex struct {
	Track     *movie.Track
	Samples   []Sample
	Wraps     int // genuine 32-bit decode-time wraparounds observed
	SyncCount int
}

// SyncSamples returns the sub-slice view of sync sample indices.
func (ti *TrackIndex) SyncSamples() []int {
	out := make([]int, 0, ti.SyncCount)
	for i := range ti.Samples {
		if ti.Samples[i].Sync {
			out = append(out, i)
		}
	}
	return out
}

// Build constructs indexes for every track. fileSize bounds sample
// offsets and sizes before any payload read can happen.
func Build(m *movie.Movie, fileSize int64) ([]*TrackIndex, error) {
	out := make([]*TrackIndex, 0, len(m.Tracks))
	for _, tr := range m.Tracks {
		ti, err := buildTrack(tr, fileSize)
		if err != nil {
			return nil, err
		}
		out = append(out, ti)
	}
	return out, nil
}

func buildTrack(tr *movie.Track, fileSize int64) (*TrackIndex, error) {
	n := len(tr.Sizes)
	ti := &TrackIndex{Track: tr, Samples: make([]Sample, n)}

	// 1. Sample file offsets from stsc + stco/co64 + stsz.
	if err := mapChunks(tr, ti.Samples, fileSize); err != nil {
		return nil, fmt.Errorf("trak %d: %w", tr.ID, err)
	}

	// 2. Decode times: accumulate stts deltas, then unwrap the raw
	//    32-bit decode-time sequence to 64-bit.
	raw := make([]uint32, n)
	var acc uint64
	i := 0
	for _, e := range tr.Stts {
		for k := uint32(0); k < e.Count && i < n; k++ {
			raw[i] = uint32(acc) // decode time as stored in 32-bit fields
			acc += uint64(e.Delta)
			i++
		}
	}
	if i != n {
		return nil, fmt.Errorf("trak %d: stts expands to %d samples, stsz has %d", tr.ID, i, n)
	}
	dts, wraps, err := timeline.Unwrap32(raw)
	if err != nil {
		return nil, fmt.Errorf("trak %d: %w", tr.ID, err)
	}
	ti.Wraps = wraps

	// 3. Composition offsets (ctts); absent table means CTS = 0.
	cts := make([]int64, n)
	i = 0
	for _, e := range tr.Ctts {
		for k := uint32(0); k < e.Count && i < n; k++ {
			cts[i] = e.Offset
			i++
		}
	}
	if len(tr.Ctts) > 0 && i != n {
		return nil, fmt.Errorf("trak %d: ctts expands to %d samples, stsz has %d", tr.ID, i, n)
	}

	// 4. Sync flags: no stss box means every sample is a sync sample.
	sync := make([]bool, n)
	if !tr.HasStss {
		for i := range sync {
			sync[i] = true
		}
	} else {
		for _, s := range tr.Stss {
			if s == 0 || int(s) > n {
				return nil, fmt.Errorf("trak %d: stss entry %d out of range [1,%d]", tr.ID, s, n)
			}
			sync[s-1] = true
		}
	}

	for j := 0; j < n; j++ {
		ti.Samples[j].Index = j
		ti.Samples[j].DTS = dts[j]
		ti.Samples[j].CTS = cts[j]
		ti.Samples[j].PTS = dts[j] + cts[j]
		ti.Samples[j].Sync = sync[j]
		if sync[j] {
			ti.SyncCount++
		}
	}
	return ti, nil
}

// mapChunks assigns file offsets to samples using stsc/stco/stsz and
// validates every extent against the file size before any read.
func mapChunks(tr *movie.Track, samples []Sample, fileSize int64) error {
	if len(tr.Stsc) == 0 {
		return fmt.Errorf("stsc is empty")
	}
	if len(tr.Chunks) == 0 {
		return fmt.Errorf("stco/co64 is empty")
	}
	sample := 0
	entry := 0
	for chunk := 0; chunk < len(tr.Chunks); chunk++ {
		// Advance the stsc run if the next run starts at this chunk (1-based).
		for entry+1 < len(tr.Stsc) && int64(tr.Stsc[entry+1].FirstChunk) <= int64(chunk)+1 {
			entry++
		}
		spc := int(tr.Stsc[entry].SamplesPerChunk)
		off := tr.Chunks[chunk]
		if off < 0 || off > fileSize {
			return fmt.Errorf("chunk %d offset %d out of file bounds %d", chunk, off, fileSize)
		}
		for k := 0; k < spc && sample < len(samples); k++ {
			size := tr.Sizes[sample]
			if size < 0 || off+size > fileSize {
				return fmt.Errorf("sample %d extent [%d,%d) exceeds file size %d", sample, off, off+size, fileSize)
			}
			samples[sample].Offset = off
			samples[sample].Size = size
			off += size
			sample++
		}
	}
	if sample != len(samples) {
		return fmt.Errorf("stsc/stco map %d samples, stsz declares %d", sample, len(samples))
	}
	return nil
}
