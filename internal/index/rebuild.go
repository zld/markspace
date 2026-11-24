package index

import (
	"io"
	"os"

	"archivist/internal/fault"
	"archivist/internal/repair"
	"archivist/internal/scan"
)

// RebuildOptions controls how the output file is produced.
type RebuildOptions struct {
	// MovieTimescale overrides the source movie timescale when non-zero.
	MovieTimescale uint32
	// ChunkSamples groups consecutive samples into one chunk. One-sample
	// chunks make every indexed offset independently seekable.
	ChunkSamples int
}

// Rebuild writes a normalized MP4 whose sample tables describe the repaired
// timeline, then returns the index with absolute output offsets populated.
//
// Timestamps in the output file are rebased so the first repaired sample sits
// at presentation time zero. This is required because a repaired timeline may
// legitimately extend past 2^32 ticks, which the MP4 sample tables cannot
// represent. The rebase amount is recorded per track so the absolute repaired
// times remain recoverable, and the returned index keeps the absolute values.
func Rebuild(src *scan.Container, rep *repair.Result, outPath string, opts RebuildOptions) (*Index, error) {
	if src == nil || rep == nil {
		return nil, fault.New(fault.KindInvariant, fault.StageIndex, "nil input to rebuild")
	}
	if opts.ChunkSamples <= 0 {
		opts.ChunkSamples = 1
	}
	movieTS := opts.MovieTimescale
	if movieTS == 0 {
		movieTS = src.MovieTimescale
	}
	if movieTS == 0 {
		return nil, fault.New(fault.KindCorrupt, fault.StageIndex,
			"movie timescale is undefined").In(src.File)
	}

	tracks, err := planTracks(src, rep, opts.ChunkSamples)
	if err != nil {
		return nil, err
	}

	ftypLen := int64(ftypLength())
	moovLen, err := moovLength(tracks, movieTS)
	if err != nil {
		return nil, err
	}
	payloadStart := ftypLen + moovLen + int64(mdatHeaderSize)
	assignOffsets(tracks, payloadStart)

	tmp := outPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return nil, fault.New(fault.KindIO, fault.StageWrite, "create output").In(tmp).Wrap(err)
	}
	fail := func(err error) (*Index, error) {
		f.Close()
		os.Remove(tmp)
		return nil, err
	}
	if err := writeFtyp(f); err != nil {
		return fail(err)
	}
	if err := writeMoov(f, tracks, movieTS); err != nil {
		return fail(err)
	}
	if err := writeMdat(f, src, tracks); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(fault.New(fault.KindIO, fault.StageWrite, "sync output").In(tmp).Wrap(err))
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return nil, fault.New(fault.KindIO, fault.StageWrite, "close output").In(tmp).Wrap(err)
	}
	if err := os.Rename(tmp, outPath); err != nil {
		os.Remove(tmp)
		return nil, fault.New(fault.KindIO, fault.StageWrite, "publish output").In(outPath).Wrap(err)
	}

	// Re-scan the file we just wrote and confirm the tables decode back to the
	// planned timeline before the index is used.
	if err := verifyRoundTrip(outPath, tracks, movieTS); err != nil {
		return nil, err
	}

	ix, err := Build(rep)
	if err != nil {
		return nil, err
	}
	offsets := make(map[uint32][]int64, len(tracks))
	bases := make(map[uint32]int64, len(tracks))
	for _, t := range tracks {
		offsets[t.trackID] = t.sampleOffsets
		bases[t.trackID] = t.outputBase
	}
	if err := ix.SampleOffsets(offsets); err != nil {
		return nil, err
	}
	if err := ix.SetRebase(bases); err != nil {
		return nil, err
	}
	return ix, nil
}

// plannedTrack carries the repaired values plus the write plan.
type plannedTrack struct {
	trackID         uint32
	timescale       uint32
	handler         string
	width, height   uint32
	outputBase      int64    // subtracted from repaired PTS to fit 32 bits
	dts             []uint64 // rebased decode times written to stts
	sizes           []uint32
	keyframes       []uint32 // 1-based
	sourceOffsets   []int64
	sampleOffsets   []int64 // absolute offsets in the rebuilt file
	chunkOffsets    []uint64
	samplesPerChunk uint32
	duration        uint64
}

func planTracks(src *scan.Container, rep *repair.Result, chunkSamples int) ([]*plannedTrack, error) {
	byID := make(map[uint32]*scan.Track, len(src.Tracks))
	for _, t := range src.Tracks {
		byID[t.ID] = t
	}
	var out []*plannedTrack
	for _, rt := range rep.Tracks {
		st, ok := byID[rt.TrackID]
		if !ok {
			return nil, fault.New(fault.KindInvariant, fault.StageIndex,
				"repair result references unknown track %d", rt.TrackID)
		}
		if len(rt.Entries) != len(st.Samples) {
			return nil, fault.New(fault.KindInvariant, fault.StageIndex,
				"track %d repair entries %d != scanned samples %d",
				rt.TrackID, len(rt.Entries), len(st.Samples))
		}
		pt := &plannedTrack{
			trackID:         rt.TrackID,
			timescale:       rt.Timescale,
			handler:         st.Handler,
			width:           st.Width,
			height:          st.Height,
			samplesPerChunk: uint32(chunkSamples),
		}
		if pt.timescale == 0 {
			return nil, fault.New(fault.KindCorrupt, fault.StageIndex,
				"track %d has a zero timescale", rt.TrackID)
		}
		base := rt.Entries[0].Repaired
		pt.outputBase = base

		var prevOut int64 = -1
		for i, e := range rt.Entries {
			smp := st.Samples[i]
			if e.Repaired < base {
				return nil, fault.New(fault.KindInvariant, fault.StageIndex,
					"track %d sample %d precedes the first sample", rt.TrackID, e.Sample)
			}
			outTime := e.Repaired - base
			if outTime > int64(^uint32(0)) {
				return nil, fault.New(fault.KindCorrupt, fault.StageWrite,
					"track %d repaired timeline exceeds 32 bits after rebasing", rt.TrackID).In(src.File)
			}
			if i > 0 && e.Repaired <= rt.Entries[i-1].Repaired {
				return nil, fault.New(fault.KindInvariant, fault.StageIndex,
					"track %d sample %d is not strictly increasing", rt.TrackID, e.Sample)
			}
			pt.dts = append(pt.dts, uint64(outTime))
			pt.sizes = append(pt.sizes, smp.Size)
			pt.sourceOffsets = append(pt.sourceOffsets, smp.Offset)
			if e.Keyframe {
				pt.keyframes = append(pt.keyframes, uint32(i+1))
			}
			if outTime > prevOut {
				pt.duration = uint64(outTime)
			}
			prevOut = outTime
		}
		// stts deltas must cover the final sample's duration as well.
		if len(pt.dts) > 0 {
			step := rt.Step
			if step <= 0 {
				step = 1
			}
			pt.duration = uint64(prevOut) + uint64(step)
		}
		out = append(out, pt)
	}
	sortPlanned(out)
	return out, nil
}

func sortPlanned(tracks []*plannedTrack) {
	for i := 1; i < len(tracks); i++ {
		for j := i; j > 0 && tracks[j].trackID < tracks[j-1].trackID; j-- {
			tracks[j], tracks[j-1] = tracks[j-1], tracks[j]
		}
	}
}

// assignOffsets packs samples into chunks in track order and records the
// absolute output offset of every sample.
func assignOffsets(tracks []*plannedTrack, payloadStart int64) {
	pos := payloadStart
	for _, t := range tracks {
		t.chunkOffsets = nil
		t.sampleOffsets = make([]int64, len(t.sizes))
		spc := int(t.samplesPerChunk)
		for i := 0; i < len(t.sizes); i += spc {
			t.chunkOffsets = append(t.chunkOffsets, uint64(pos))
			for j := i; j < i+spc && j < len(t.sizes); j++ {
				t.sampleOffsets[j] = pos
				pos += int64(t.sizes[j])
			}
		}
	}
}

func mdatPayloadSize(tracks []*plannedTrack) int64 {
	var total int64
	for _, t := range tracks {
		for _, s := range t.sizes {
			total += int64(s)
		}
	}
	return total
}

// writeMdat copies every source sample payload in the planned order.
func writeMdat(f *os.File, src *scan.Container, tracks []*plannedTrack) error {
	in, err := os.Open(src.File)
	if err != nil {
		return fault.New(fault.KindIO, fault.StageRead, "open source for payload copy").In(src.File).Wrap(err)
	}
	defer in.Close()
	payloadLen := mdatPayloadSize(tracks)
	boxSize := uint64(payloadLen) + mdatHeaderSize
	if boxSize > 0xFFFFFFFF {
		return fault.New(fault.KindCorrupt, fault.StageWrite,
			"output exceeds 4GiB; co64 layout is not supported").In(src.File)
	}
	if _, err := f.Write(boxHeader("mdat", boxSize)); err != nil {
		return fault.New(fault.KindIO, fault.StageWrite, "write mdat header").Wrap(err)
	}
	buf := make([]byte, 64*1024)
	for _, t := range tracks {
		for i, off := range t.sourceOffsets {
			if _, err := in.Seek(off, io.SeekStart); err != nil {
				return fault.New(fault.KindIO, fault.StageRead, "seek source sample").In(src.File).Wrap(err)
			}
			remaining := int64(t.sizes[i])
			for remaining > 0 {
				chunk := int64(len(buf))
				if remaining < chunk {
					chunk = remaining
				}
				if _, err := io.ReadFull(in, buf[:chunk]); err != nil {
					return fault.New(fault.KindIO, fault.StageRead, "read source sample").In(src.File).Wrap(err)
				}
				if _, err := f.Write(buf[:chunk]); err != nil {
					return fault.New(fault.KindIO, fault.StageWrite, "write output sample").Wrap(err)
				}
				remaining -= chunk
			}
		}
	}
	return nil
}
