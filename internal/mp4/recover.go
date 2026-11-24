package mp4

import (
	"fmt"

	mp4 "github.com/abema/go-mp4"

	"regional.example/mediad/internal/clock"
	"regional.example/mediad/internal/fault"
)

// buildTrack turns raw container tables into confirmed sample extents.
// A sample is confirmed only when its size is a whole number of PCM frames,
// its byte range lies inside the readable file, and its chunk offset does not
// regress past the running end of the previous chunk.
func buildTrack(t structTrack, readable, maxSamples, mdatStart int64) (TrackInfo, []fault.Fault, error) {
	ti := TrackInfo{
		ID:             t.id,
		Handler:        t.handler,
		MediaTimescale: t.mediaTimescale,
		MediaDuration:  t.mediaDuration,
		Channels:       t.channels,
		SampleSize:     t.sampleSizeBits,
		SampleRate:     t.sampleRate,
		Codec:          t.sampleType,
	}
	var fs []fault.Fault
	if t.handler != "soun" {
		ti.Layout = ""
		return ti, fs, nil
	}
	if t.mediaTimescale == 0 {
		return ti, nil, fault.Fatal(fault.ClassContainer, "timescale", "audio track has a zero media timescale").OnTrack(t.id)
	}
	switch t.sampleType {
	case "ipcm", "fpcm":
	default:
		return ti, nil, fault.Fatal(fault.ClassAudio, "codec-unsupported",
			fmt.Sprintf("audio sample entry %q is not linear PCM", t.sampleType)).OnTrack(t.id)
	}
	if t.channels <= 0 {
		return ti, nil, fault.Fatal(fault.ClassAudio, "channels", "audio sample entry declares no channels").OnTrack(t.id)
	}
	if t.sampleSizeBits != 16 {
		return ti, nil, fault.Fatal(fault.ClassAudio, "bit-depth",
			fmt.Sprintf("bit depth %d is unsupported (only 16)", t.sampleSizeBits)).OnTrack(t.id)
	}
	blockAlign := int64(t.channels) * int64(t.sampleSizeBits/8)
	ti.Layout = layoutForChannels(t.channels)

	n := sampleCount(t)
	if maxSamples > 0 && int64(n) > maxSamples {
		return ti, nil, fault.Fatal(fault.ClassLimit, "sample-count",
			fmt.Sprintf("%d samples exceeds cap %d", n, maxSamples)).OnTrack(t.id)
	}
	if n == 0 {
		return ti, []fault.Fault{fault.New(fault.ClassIndex, "empty-track", "track declares no samples").OnTrack(t.id)}, nil
	}

	sizes := make([]uint32, n)
	deltas := make([]int64, n)
	switch {
	case t.stszConstant != 0:
		for i := range sizes {
			sizes[i] = t.stszConstant
		}
		ti.ConstantSize = int64(t.stszConstant)
	case len(t.stszSizes) > 0:
		copy(sizes, t.stszSizes)
	default:
		fs = append(fs, fault.New(fault.ClassIndex, "stsz-missing",
			"sample size table is absent; sizes default to the declared frame size").OnTrack(t.id))
	}
	// Expand stts runs into per-sample deltas.
	idx := 0
	for _, e := range t.stts {
		for k := uint32(0); k < e.SampleCount && idx < n; k++ {
			deltas[idx] = int64(e.SampleDelta)
			idx++
		}
	}
	if idx < n {
		fs = append(fs, fault.New(fault.ClassIndex, "stts-short",
			fmt.Sprintf("stts covers %d of %d samples", idx, n)).OnTrack(t.id))
	}
	var mediaTicks int64
	for i := 0; i < n; i++ {
		if deltas[i] <= 0 {
			deltas[i] = 1 // degenerate entry; confirmed by timeline validation
		}
		mediaTicks += deltas[i]
	}
	ti.MediaTicks = mediaTicks

	perChunk, indexMissing, err := expandChunks(t, n)
	if err != nil {
		return ti, nil, err
	}
	if indexMissing {
		fs = append(fs, fault.New(fault.ClassIndex, "index-missing",
			"chunk offset table is absent; rebuilding sequential layout from mdat").OnTrack(t.id))
	}

	// Chunk offsets: declared when present, synthesized from mdat otherwise.
	// totalChunks counts every chunk position, including zero-sample runs, so
	// each offset entry stays 1:1 with the chunk it describes.  Dropping a zero
	// run from the count would shift every later offset onto the wrong chunk.
	offsets := make([]int64, 0, len(perChunk))
	totalChunks := len(perChunk)
	switch {
	case len(t.stco) >= totalChunks && !indexMissing:
		offsets = append(offsets, toInt64(t.stco[:totalChunks])...)
	case len(t.co64) >= totalChunks && !indexMissing:
		offsets = append(offsets, toInt64_64(t.co64[:totalChunks])...)
	default:
		// The table is absent, too short, or overflowing; rebuild sequentially,
		// but only from a real mdat payload.  Without an mdat there is nothing
		// to rebuild from and confirming samples over ftyp/moov would be a
		// fabricated extent.
		if mdatStart < 0 {
			return ti, []fault.Fault{fault.New(fault.ClassIndex, "no-mdat",
				"chunk index is missing and the file has no media payload to rebuild from").OnTrack(t.id)}, fmt.Errorf("%w", fault.New(fault.ClassIndex, "no-mdat", "no mdat to rebuild from").OnTrack(t.id))
		}
		if indexMissing {
			// already recorded by expandChunks
		} else {
			fs = append(fs, fault.New(fault.ClassIndex, "index-short",
				fmt.Sprintf("chunk table has %d entries for %d chunks; rebuilding from mdat", max64(int64(len(t.stco)), int64(len(t.co64))), totalChunks)).OnTrack(t.id))
		}
		indexMissing = true
		cursor := mdatStart
		for c := 0; c < totalChunks; c++ {
			offsets = append(offsets, cursor)
			cursor += chunkPayloadLen(sizes, perChunk, c)
		}
	}

	// Place samples and confirm extents.
	chunks := make([]Chunk, 0, totalChunks)
	samples := make([]Sample, 0, n)
	cursor := 0
	var prevChunkEnd int64
	var mediaTick int64
	for ci := 0; ci < totalChunks; ci++ {
		per := perChunk[ci]
		if per <= 0 {
			continue
		}
		off := offsets[ci]
		chunkStart := off
		// Compare against the high-water mark of all earlier chunks, not just
		// the adjacent one: a short overlapping chunk must not lower the bar
		// and let a later chunk overlap an older one unnoticed.
		overlapped := ci > 0 && off < prevChunkEnd
		if overlapped {
			fs = append(fs, fault.New(fault.ClassIndex, "chunk-overlap",
				fmt.Sprintf("chunk %d offset %d overlaps previous chunk ending at %d", ci, off, prevChunkEnd)).
				OnTrack(t.id).AtByte(off))
			// Deterministic repair: keep the later chunk where the table says
			// and mark its samples unconfirmable rather than silently dropping.
		}
		ch := Chunk{Index: ci, Offset: chunkStart}
		broken := false // once a boundary fails, later ones in this chunk are unknown
		for k := 0; k < per && cursor < n; k++ {
			sz := int64(sizes[cursor])
			s := Sample{Index: cursor, Offset: off, Size: sz, Delta: deltas[cursor], MediaTick: mediaTick}
			confirmed := true
			if sz == 0 || sz%blockAlign != 0 {
				broken = true
				confirmed = false
				fs = append(fs, fault.New(fault.ClassSample, "unaligned-sample",
					fmt.Sprintf("sample %d size %d is not a whole %d-byte PCM frame", cursor, sz, blockAlign)).
					OnTrack(t.id).AtByte(off))
			}
			// Overflow-safe range check: a hostile co64 offset near MaxInt64
			// must not wrap off+sz negative and slip through as in-file.
			if off < 0 || sz < 0 || sz > readable || off > readable-sz {
				confirmed = false
				broken = true
				fs = append(fs, fault.New(fault.ClassSample, "truncated-sample",
					fmt.Sprintf("sample %d range starting at %d exceeds readable bytes %d", cursor, off, readable)).
					OnTrack(t.id).AtByte(readable))
			}
			if overlapped {
				confirmed = false
				broken = true
			}
			if broken {
				confirmed = false
			}
			s.Confirmed = confirmed
			samples = append(samples, s)
			ch.Samples = append(ch.Samples, cursor)
			// Advance with saturation so a wrapped offset cannot alias to a
			// valid-looking position later in the same chunk.
			if sz > 0 && off <= maxInt64-sz {
				off += sz
			} else {
				off = maxInt64
			}
			mediaTick += deltas[cursor]
			cursor++
		}
		ch.Size = off - chunkStart
		chunks = append(chunks, ch)
		if off > prevChunkEnd {
			prevChunkEnd = off
		}
	}
	ti.Samples = samples
	ti.Chunks = chunks
	ti.Edits = t.edits
	ti.SyncSamples = t.syncSamples
	if len(t.syncSamples) > 0 && n > 0 {
		ti.Sparse = n/len(t.syncSamples) >= 8
	}
	sortFaults(fs)
	return ti, fs, nil
}

func sampleCount(t structTrack) int {
	if t.stszCount > 0 {
		return int(t.stszCount)
	}
	total := 0
	for _, e := range t.stts {
		total += int(e.SampleCount)
	}
	return total
}

// expandChunks returns the number of samples per chunk for every chunk, in
// order, plus whether the offset table itself was missing.
func expandChunks(t structTrack, sampleCount int) (perChunk []int, indexMissing bool, err error) {
	if sampleCount <= 0 {
		return nil, len(t.stco) == 0 && len(t.co64) == 0, nil
	}
	if len(t.stsc) == 0 {
		return nil, true, fault.Fatal(fault.ClassIndex, "stsc-missing", "sample-to-chunk table is absent").OnTrack(t.id)
	}
	indexMissing = len(t.stco) == 0 && len(t.co64) == 0

	chunkCount := len(t.stco)
	if chunkCount == 0 {
		chunkCount = len(t.co64)
	}
	per := make([]int, 0, chunkCount)
	placed := 0
	if chunkCount > 0 {
		for c := 1; c <= chunkCount; c++ {
			run := stscRunAt(t.stsc, c)
			if run.SamplesPerChunk == 0 {
				// A zero-length run is malformed; emit a zero-count chunk so
				// the offset table stays aligned with the chunk numbering and
				// later chunks keep their declared offsets.
				per = append(per, 0)
				continue
			}
			k := int(run.SamplesPerChunk)
			if placed+k > sampleCount {
				k = sampleCount - placed
			}
			per = append(per, k)
			placed += k
		}
		if placed < sampleCount {
			per = append(per, sampleCount-placed)
		}
	} else {
		c := 1
		zeroRuns := 0
		for placed < sampleCount {
			run := stscRunAt(t.stsc, c)
			k := int(run.SamplesPerChunk)
			if k <= 0 {
				// Skip a malformed zero run, but bound the loop so a table of
				// zeros cannot spin forever without placing a sample.
				c++
				zeroRuns++
				if zeroRuns > sampleCount+len(t.stsc)+1 {
					break
				}
				continue
			}
			zeroRuns = 0
			if placed+k > sampleCount {
				k = sampleCount - placed
			}
			per = append(per, k)
			placed += k
			c++
		}
		// Any samples the runs did not cover are laid out as a final chunk so
		// the track is never silently dropped.
		if placed < sampleCount {
			per = append(per, sampleCount-placed)
		}
	}
	return per, indexMissing, nil
}

func stscRunAt(entries []mp4.StscEntry, chunk int) mp4.StscEntry {
	// Start with the run whose FirstChunk is the largest that is <= chunk.
	// A table whose first run does not begin at chunk 1 is malformed; using
	// entries[0] for earlier chunks would fabricate sample density, so the
	// effective run for chunk 1 is the earliest declared run and the caller's
	// count check keeps placement consistent.
	cur := entries[0]
	for _, e := range entries {
		if int(e.FirstChunk) <= chunk && int(e.FirstChunk) >= int(cur.FirstChunk) {
			cur = e
		}
	}
	return cur
}

func chunkPayloadLen(sizes []uint32, perChunk []int, chunk int) int64 {
	lo := 0
	for i := 0; i < chunk; i++ {
		lo += perChunk[i]
	}
	hi := lo + perChunk[chunk]
	if hi > len(sizes) {
		hi = len(sizes)
	}
	var sum int64
	for i := lo; i < hi; i++ {
		sum += int64(sizes[i])
	}
	return sum
}

func toInt64(in []uint32) []int64 {
	out := make([]int64, len(in))
	for i, v := range in {
		out[i] = int64(v)
	}
	return out
}

func toInt64_64(in []uint64) []int64 {
	out := make([]int64, len(in))
	for i, v := range in {
		if v > uint64(maxInt64) {
			// A chunk offset beyond int64 cannot be a real file position; clamp
			// to a value the range check will reject rather than wrap negative.
			out[i] = maxInt64
			continue
		}
		out[i] = int64(v)
	}
	return out
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// BuildTimeline converts confirmed samples of the primary audio track into
// presentation intervals.  Unconfirmed samples become explicit gaps; the
// timeline starts at the requested tick so non-zero origins survive.
func BuildTimeline(res *ScanResult, startTick int64) (*clock.Timeline, error) {
	rate := clock.Rate{}
	if res.MovieTimescale > 0 {
		r, err := clock.NewRate(int64(res.MovieTimescale), 1)
		if err != nil {
			return nil, err
		}
		rate = r
	}
	ti := primaryTrack(res)
	if ti == nil {
		// A manifest with no audio track still needs a valid empty timeline.
		if !rate.IsValid() {
			rate = clock.Rate48000
		}
		tl, err := clock.NewTimeline(rate)
		if err != nil {
			return nil, err
		}
		if err := tl.SetStart(startTick); err != nil {
			return nil, err
		}
		return tl, nil
	}
	mediaRate, err := clock.NewRate(int64(ti.MediaTimescale), 1)
	if err != nil {
		return nil, err
	}
	if !rate.IsValid() {
		rate = mediaRate
	}
	tl, err := clock.NewTimeline(rate)
	if err != nil {
		return nil, err
	}
	if err := tl.SetStart(startTick); err != nil {
		return nil, err
	}

	// Walk samples in media order.  Confirmed runs are rescaled from media
	// ticks to the movie timescale without floating point.
	for _, s := range ti.Samples {
		dur, err := clock.Rescale(s.Delta, mediaRate, rate)
		if err != nil {
			return nil, err
		}
		if s.Confirmed {
			if _, err := tl.AddMedia("track-"+itoa(ti.ID), s.MediaTick, dur); err != nil {
				return nil, err
			}
		} else {
			if _, err := tl.AddGap(dur); err != nil {
				return nil, err
			}
		}
	}
	// Trailing missing time: the declared media duration may exceed confirmed
	// samples after truncation; declare the remainder as a gap.
	if ti.MediaDuration > ti.MediaTicks {
		extra, err := clock.Rescale(ti.MediaDuration-ti.MediaTicks, mediaRate, rate)
		if err != nil {
			return nil, err
		}
		if extra > 0 {
			if _, err := tl.AddGap(extra); err != nil {
				return nil, err
			}
		}
	}
	if err := tl.Validate(); err != nil {
		return nil, fault.Fatal(fault.ClassTimeline, "timeline", err.Error()).OnTrack(ti.ID).Because(err)
	}
	return tl, nil
}

func primaryTrack(res *ScanResult) *TrackInfo {
	for i := range res.Tracks {
		if res.Tracks[i].Handler == "soun" {
			return &res.Tracks[i]
		}
	}
	return nil
}

func itoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
