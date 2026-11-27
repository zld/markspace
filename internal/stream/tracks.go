package stream

import (
	"fmt"
	"sort"

	mp4 "github.com/abema/go-mp4"

	"archivecheck/internal/asset"
	"archivecheck/internal/fault"
	"archivecheck/internal/timebase"
)

// parseMovie extracts tracks and keyframe data from moov. Unreadable metadata
// is recorded as damage rather than aborting the whole analysis.
func parseMovie(src *asset.Source, moov *mp4Box, s *Structure) {
	for _, trak := range moov.children {
		if trak.typ != "trak" {
			continue
		}
		s.Tracks = append(s.Tracks, parseTrack(src, trak))
	}
	sort.SliceStable(s.Tracks, func(i, j int) bool { return s.Tracks[i].ID < s.Tracks[j].ID })
}

func parseTrack(src *asset.Source, trak *mp4Box) Track {
	data := src.Bytes()
	t := Track{}
	var tkhd mp4.Tkhd
	if unmarshal(data, child(trak, "tkhd"), &tkhd) {
		t.ID = tkhd.TrackID
	}
	var mdhd mp4.Mdhd
	if unmarshal(data, child(trak, "mdia", "mdhd"), &mdhd) {
		t.Timescale = mdhd.Timescale
	}
	var hdlr mp4.Hdlr
	if unmarshal(data, child(trak, "mdia", "hdlr"), &hdlr) {
		t.Handler = printable(string(hdlr.HandlerType[:]))
	}
	stbl := child(trak, "mdia", "minf", "stbl")
	if stbl == nil {
		t.Damaged = append(t.Damaged, fault.Span{Start: trak.offset, End: min64(src.Size, trak.offset+1),
			Kind: fault.KindMissing, Detail: fmt.Sprintf("track %d has no stbl box", t.ID)})
		return t
	}
	fillSamples(data, stbl, &t)
	return t
}

// fillSamples reads stsz, stco/co64, stsc, stts and stss to derive the sample
// count, keyframe byte offsets and keyframe timestamps on the nanosecond scale.
func fillSamples(data []byte, stbl *mp4Box, t *Track) {
	mark := func(box *mp4Box, kind, format string, args ...any) {
		start := stbl.offset
		if box != nil {
			start = box.offset
		}
		t.Damaged = append(t.Damaged, fault.Span{Start: start, End: start + 1, Kind: kind,
			Detail: fmt.Sprintf("track %d: %s", t.ID, fmt.Sprintf(format, args...))})
	}
	var stsz mp4.Stsz
	if !unmarshal(data, child(stbl, "stsz"), &stsz) {
		mark(child(stbl, "stsz"), fault.KindMissing, "no readable stsz box")
		return
	}
	count := int(stsz.SampleCount)
	t.SampleCount = count
	sizes := make([]int64, count)
	for i := 0; i < count; i++ {
		switch {
		case stsz.SampleSize != 0:
			sizes[i] = int64(stsz.SampleSize)
		case i < len(stsz.EntrySize):
			sizes[i] = int64(stsz.EntrySize[i])
		default:
			mark(child(stbl, "stsz"), fault.KindCorrupt, "stsz describes %d sizes for %d samples", len(stsz.EntrySize), count)
			return
		}
	}
	offsets, ok := chunkOffsets(data, stbl)
	if !ok {
		mark(child(stbl, "stco"), fault.KindMissing, "no readable chunk offset table")
		return
	}
	var stsc mp4.Stsc
	if !unmarshal(data, child(stbl, "stsc"), &stsc) {
		mark(child(stbl, "stsc"), fault.KindMissing, "no readable stsc box")
		return
	}
	sampleOffsets, laidOut := layOutSamples(offsets, sizes, &stsc)
	if laidOut != count {
		mark(child(stbl, "stsc"), fault.KindCorrupt, "chunk tables describe %d of %d samples", laidOut, count)
		return
	}

	var stts mp4.Stts
	if !unmarshal(data, child(stbl, "stts"), &stts) {
		mark(child(stbl, "stts"), fault.KindMissing, "no readable stts box")
		return
	}
	times, described := decodeSTTS(&stts, count)
	if described != count {
		mark(child(stbl, "stts"), fault.KindCorrupt, "stts describes %d of %d samples", described, count)
		return
	}
	toNanos := func(units int64) timebase.Nanos {
		if t.Timescale == 0 {
			return 0
		}
		return scaleToNanos(units, int64(t.Timescale))
	}
	var total int64
	if count > 0 {
		if len(stts.Entries) > 0 {
			last := stts.Entries[len(stts.Entries)-1]
			for _, e := range stts.Entries {
				total += int64(e.SampleCount) * int64(e.SampleDelta)
			}
			_ = last
		}
		t.DurationNanos = toNanos(total)
	}

	stssBox := child(stbl, "stss")
	numbers := make([]int, 0, count)
	if stssBox == nil {
		for i := 1; i <= count; i++ {
			numbers = append(numbers, i)
		}
	} else {
		var stss mp4.Stss
		if !unmarshal(data, stssBox, &stss) {
			mark(stssBox, fault.KindCorrupt, "stss is unreadable")
			return
		}
		for _, n := range stss.SampleNumber {
			numbers = append(numbers, int(n))
		}
	}
	t.SyncSamples = len(numbers)
	for _, n := range numbers {
		if n < 1 || n > count {
			mark(stssBox, fault.KindCorrupt, "sync sample %d outside 1..%d", n, count)
			continue
		}
		t.Keyframes = append(t.Keyframes, Keyframe{Number: n, Offset: sampleOffsets[n-1], Time: toNanos(times[n-1])})
	}
	for i := 1; i < len(t.Keyframes); i++ {
		t.Intervals = append(t.Intervals, t.Keyframes[i].Time-t.Keyframes[i-1].Time)
	}
	if len(t.Intervals) > 0 {
		var sum timebase.Nanos
		for _, d := range t.Intervals {
			sum += d
		}
		t.Interval = sum / timebase.Nanos(len(t.Intervals))
	}
}

func chunkOffsets(data []byte, stbl *mp4Box) ([]int64, bool) {
	if box := child(stbl, "stco"); box != nil {
		var stco mp4.Stco
		if !unmarshal(data, box, &stco) {
			return nil, false
		}
		out := make([]int64, len(stco.ChunkOffset))
		for i, v := range stco.ChunkOffset {
			out[i] = int64(v)
		}
		return out, true
	}
	if box := child(stbl, "co64"); box != nil {
		var co64 mp4.Co64
		if !unmarshal(data, box, &co64) {
			return nil, false
		}
		out := make([]int64, len(co64.ChunkOffset))
		for i, v := range co64.ChunkOffset {
			out[i] = int64(v)
		}
		return out, true
	}
	return nil, false
}

// layOutSamples maps every sample to its absolute byte offset assuming samples
// are contiguous within a chunk. It returns the offsets and the number of
// samples successfully placed.
func layOutSamples(chunkOffsets, sizes []int64, stsc *mp4.Stsc) ([]int64, int) {
	out := make([]int64, len(sizes))
	si := 0
	for ci, chunkStart := range chunkOffsets {
		if si >= len(sizes) {
			break
		}
		spc := samplesInChunk(stsc, ci)
		pos := chunkStart
		for k := 0; k < spc && si < len(sizes); k++ {
			out[si] = pos
			pos += sizes[si]
			si++
		}
	}
	return out, si
}

func samplesInChunk(stsc *mp4.Stsc, chunk int) int {
	best := 0
	for _, e := range stsc.Entries {
		if int(e.FirstChunk)-1 <= chunk {
			best = int(e.SamplesPerChunk)
		} else {
			break
		}
	}
	return best
}

// decodeSTTS expands the run-length time-to-sample table into per-sample
// decode timestamps at the media timescale.
func decodeSTTS(stts *mp4.Stts, count int) ([]int64, int) {
	out := make([]int64, count)
	cursor := 0
	var ts int64
	for _, e := range stts.Entries {
		for k := uint32(0); k < e.SampleCount && cursor < count; k++ {
			out[cursor] = ts
			ts += int64(e.SampleDelta)
			cursor++
		}
	}
	return out, cursor
}

// scaleToNanos converts units at scale units-per-second onto the shared scale.
func scaleToNanos(units, scale int64) timebase.Nanos {
	if scale <= 0 || units < 0 {
		return 0
	}
	sec := units / scale
	rem := units % scale
	return timebase.Nanos(sec*1e9 + rem*1e9/scale)
}

func printable(t string) string {
	out := make([]byte, 0, 4)
	for i := 0; i < len(t) && i < 4; i++ {
		c := t[i]
		if c < 0x20 || c > 0x7e {
			c = '.'
		}
		out = append(out, c)
	}
	return string(out)
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
