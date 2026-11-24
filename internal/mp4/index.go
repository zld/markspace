// Package mp4 reads MP4 sample tables and keyframe indexes, falling back to a
// scan of the sample table when an index is missing or out of range.
package mp4

import (
	"bytes"
	"fmt"
	"io"

	"mediaaudit/internal/model"

	mp4 "github.com/abema/go-mp4"
)

// Sample is one decoded-or-at-least-addressable sample in presentation order.
type Sample struct {
	Index       int
	TrackID     uint32
	Timescale   uint32
	DecodeTime  model.Rat // DTS in seconds
	PresentTime model.Rat // PTS in seconds (DTS + ctts offset)
	Duration    model.Rat
	Size        uint32
	Offset      uint64 // byte offset of the sample payload in the file
	Keyframe    bool
	Origin      Origin
}

// Origin says how the keyframe flag was determined.
type Origin string

const (
	OriginIndex Origin = "index"
	OriginScan  Origin = "scan"
	OriginNone  Origin = "none"
)

// TrackInfo summarises a parsed video track.
type TrackInfo struct {
	TrackID       uint32
	Timescale     uint32
	Handler       string
	Width, Height uint16
	SampleCount   int
	Keyframes     int
	// KeyframeOrigin is "index" when stss was usable, "scan" when it was
	// missing or out of range and the sample table was consulted.
	KeyframeOrigin Origin
	// FallbackReason explains why the index was not used. It is empty when the
	// index was honoured.
	FallbackReason string
	// BadIndexEntry is the 1-based stss entry that failed validation, and
	// BadIndexSample the sample number it named. Both are zero when not
	// applicable, and let the audit point at the damaged record.
	BadIndexEntry  int
	BadIndexSample uint32
	// BadIndexOffset is the byte offset of the damaged stss entry.
	BadIndexOffset uint64
}

// Index is the parse result for one MP4 file.
type Index struct {
	Tracks []TrackInfo
	byID   map[uint32][]Sample
	File   string
}

// Samples returns the presentation-ordered samples of a track.
func (ix *Index) Samples(trackID uint32) []Sample { return ix.byID[trackID] }

// VideoTrack returns the first video track, or false.
func (ix *Index) VideoTrack() (TrackInfo, bool) {
	for _, t := range ix.Tracks {
		if t.Handler == "vide" {
			return t, true
		}
	}
	return TrackInfo{}, false
}

// Options tune index reading.
type Options struct {
	// FallbackScan enables scanning the sample table when stss is absent or
	// names samples outside the track. It is on by default in the pipeline.
	FallbackScan bool
}

// Parse reads the box tree once, extracting per-track tables.
func Parse(r io.ReadSeeker, file string, opts Options) (*Index, model.Errors) {
	var es model.Errors
	ix := &Index{File: file, byID: map[uint32][]Sample{}}

	bips, err := mp4.ExtractBoxesWithPayload(r, nil, []mp4.BoxPath{
		{mp4.BoxTypeFtyp()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeTkhd()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeHdlr()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeAvc1()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeMp4a()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStts()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeCtts()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsz()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStco()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeCo64()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsc()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStss()},
	})
	if err != nil {
		es.Add(model.NewError(model.CodeIndexCorrupt, "mp4", "", model.InputPos{File: file},
			"cannot read MP4 box tree: %v", err))
		return ix, es
	}

	if !hasFtyp(r) {
		es.Add(model.NewError(model.CodeIndexCorrupt, "mp4", "", model.InputPos{File: file, Offset: 0},
			"no ftyp box; not an MP4 file"))
		return ix, es
	}

	tracks := groupByTrak(bips)
	for _, tr := range tracks {
		info, samples, terr := buildTrack(r, tr, file, opts)
		if terr != nil {
			es.Add(terr)
			continue
		}
		ix.Tracks = append(ix.Tracks, info)
		ix.byID[info.TrackID] = samples
	}
	if len(ix.Tracks) == 0 {
		if len(es) == 0 {
			es.Add(model.NewError(model.CodeIndexCorrupt, "mp4", "", model.InputPos{File: file},
				"no tracks found"))
		}
		return ix, es
	}
	return ix, es
}

// hasFtyp checks the file's first box. go-mp4 treats unrecognisable leading
// bytes as EOF, so this explicit check is what distinguishes a corrupt file
// from an empty-but-valid one.
func hasFtyp(r io.ReadSeeker) bool {
	var hdr [8]byte
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return false
	}
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return false
	}
	return string(hdr[4:8]) == "ftyp"
}

// trakBoxes holds the boxes of a single trak in extraction order.
type trakBoxes struct {
	stssOffset uint64 // file offset of the stss payload
	tkhd       *mp4.Tkhd
	mdhd       *mp4.Mdhd
	hdlr       *mp4.Hdlr
	avc1       *mp4.VisualSampleEntry
	mp4a       *mp4.AudioSampleEntry
	stts       *mp4.Stts
	ctts       *mp4.Ctts
	stsz       *mp4.Stsz
	stco       *mp4.Stco
	co64       *mp4.Co64
	stsc       *mp4.Stsc
	stss       *mp4.Stss
}

// groupByTrak splits the flat extraction list into per-trak groups. Each trak
// starts at a tkhd box, which is always present.
func groupByTrak(bips []*mp4.BoxInfoWithPayload) []*trakBoxes {
	var out []*trakBoxes
	for _, bip := range bips {
		switch b := bip.Payload.(type) {
		case *mp4.Tkhd:
			out = append(out, &trakBoxes{tkhd: b})
			continue
		}
		if len(out) == 0 {
			continue
		}
		cur := out[len(out)-1]
		switch b := bip.Payload.(type) {
		case *mp4.Mdhd:
			cur.mdhd = b
		case *mp4.Hdlr:
			cur.hdlr = b
		case *mp4.VisualSampleEntry:
			cur.avc1 = b
		case *mp4.AudioSampleEntry:
			cur.mp4a = b
		case *mp4.Stts:
			cur.stts = b
		case *mp4.Ctts:
			cur.ctts = b
		case *mp4.Stsz:
			cur.stsz = b
		case *mp4.Stco:
			cur.stco = b
		case *mp4.Co64:
			cur.co64 = b
		case *mp4.Stsc:
			cur.stsc = b
		case *mp4.Stss:
			cur.stss = b
			cur.stssOffset = bip.Info.Offset + bip.Info.HeaderSize
		}
	}
	return out
}

// buildTrack materialises samples with byte offsets and keyframe flags.
func buildTrack(r io.ReadSeeker, tr *trakBoxes, file string, opts Options) (TrackInfo, []Sample, *model.Error) {
	var info TrackInfo
	if tr.tkhd == nil || tr.mdhd == nil {
		return info, nil, model.NewError(model.CodeIndexCorrupt, "mp4", "", model.InputPos{File: file},
			"track is missing tkhd/mdhd")
	}
	info.TrackID = tr.tkhd.TrackID
	info.Timescale = tr.mdhd.Timescale
	if tr.hdlr != nil {
		info.Handler = string(bytes.TrimRight(tr.hdlr.HandlerType[:], "\x00"))
	}
	if info.Timescale == 0 {
		return info, nil, model.NewError(model.CodeIndexCorrupt, "mp4", "", model.InputPos{File: file},
			"track %d has zero timescale", info.TrackID)
	}
	if tr.avc1 != nil {
		info.Width, info.Height = tr.avc1.Width, tr.avc1.Height
	}
	if tr.stts == nil || tr.stsz == nil {
		return info, nil, model.NewError(model.CodeIndexCorrupt, "mp4", "", model.InputPos{File: file},
			"track %d is missing stts/stsz", info.TrackID)
	}

	deltas, err := expandStts(tr.stts)
	if err != nil {
		return info, nil, model.NewError(model.CodeIndexCorrupt, "mp4", "", model.InputPos{File: file},
			"track %d stts: %v", info.TrackID, err)
	}
	count := len(deltas)
	if int(tr.stsz.SampleCount) != count && tr.stsz.SampleSize == 0 {
		// A truncated stsz entry table shows up here.
		return info, nil, model.NewError(model.CodeIndexCorrupt, "mp4", "", model.InputPos{File: file},
			"track %d stsz declares %d samples but stts describes %d",
			info.TrackID, tr.stsz.SampleCount, count)
	}
	sizes := make([]uint32, count)
	for i := range sizes {
		sizes[i] = sampleSize(tr.stsz, i)
	}
	offsets, err := sampleOffsets(tr.stsc, tr.stco, tr.co64, sizes)
	if err != nil {
		return info, nil, model.NewError(model.CodeIndexCorrupt, "mp4", "", model.InputPos{File: file},
			"track %d chunk tables: %v", info.TrackID, err)
	}
	ctts, err := expandCtts(tr.ctts, count)
	if err != nil {
		return info, nil, model.NewError(model.CodeIndexCorrupt, "mp4", "", model.InputPos{File: file},
			"track %d ctts: %v", info.TrackID, err)
	}

	samples := make([]Sample, count)
	var dts int64
	for i := 0; i < count; i++ {
		dur := int64(deltas[i])
		dtsRat, rerr := model.NewRat(dts, int64(info.Timescale))
		if rerr != nil {
			return info, nil, model.NewError(model.CodeInternal, "mp4", "", model.InputPos{File: file}, "%v", rerr)
		}
		durRat, rerr := model.NewRat(dur, int64(info.Timescale))
		if rerr != nil {
			return info, nil, model.NewError(model.CodeInternal, "mp4", "", model.InputPos{File: file}, "%v", rerr)
		}
		pts := dtsRat
		if ctts != nil {
			off, rerr := model.NewRat(ctts[i], int64(info.Timescale))
			if rerr != nil {
				return info, nil, model.NewError(model.CodeInternal, "mp4", "", model.InputPos{File: file}, "%v", rerr)
			}
			pts = dtsRat.Add(off)
		}
		samples[i] = Sample{
			Index: i, TrackID: info.TrackID, Timescale: info.Timescale,
			DecodeTime: dtsRat, PresentTime: pts, Duration: durRat,
			Size: sizes[i], Offset: offsets[i], Keyframe: false,
			Origin: OriginNone,
		}
		dts += dur
	}

	// Keyframe resolution: prefer stss, fall back to a scan when it is
	// missing, malformed or names out-of-range samples.
	markKeyframes(samples, info.TrackID, tr.stss, tr.stssOffset, file, opts, &info)
	info.SampleCount = len(samples)
	return info, samples, nil
}

// markKeyframes sets Keyframe/Origin and KeyframeOrigin, recording exactly
// why the index was rejected when it was.
func markKeyframes(samples []Sample, trackID uint32, stss *mp4.Stss, stssPayloadOffset uint64, file string, opts Options, info *TrackInfo) {
	switch {
	case stss == nil:
		info.FallbackReason = "stss_missing"
	case len(stss.SampleNumber) == 0:
		info.FallbackReason = "stss_empty"
	default:
		seen := make([]bool, len(samples))
		bad := false
		for i, n := range stss.SampleNumber {
			if n == 0 || int(n) > len(samples) {
				info.FallbackReason = "stss_sample_out_of_range"
				info.BadIndexEntry = i + 1
				info.BadIndexSample = n
				// Entries are 4 bytes after the 4-byte version/flags and
				// the 4-byte entry count.
				info.BadIndexOffset = stssPayloadOffset + 8 + uint64(i)*4
				bad = true
				break
			}
			seen[n-1] = true
		}
		if !bad {
			for i := range samples {
				if seen[i] {
					samples[i].Keyframe = true
					samples[i].Origin = OriginIndex
				}
			}
			info.KeyframeOrigin = OriginIndex
			info.Keyframes = countTrue(seen)
			return
		}
	}
	if !opts.FallbackScan {
		info.KeyframeOrigin = OriginNone
		return
	}
	// Scan fallback: treat every sample whose payload is addressable and
	// non-empty as a candidate sync sample. This is the documented, honest
	// degradation when stss is missing or corrupt; Origin records the reason.
	for i := range samples {
		samples[i].Keyframe = samples[i].Size > 0
		samples[i].Origin = OriginScan
		if samples[i].Keyframe {
			info.Keyframes++
		}
	}
	info.KeyframeOrigin = OriginScan
}

func countTrue(b []bool) int {
	n := 0
	for _, v := range b {
		if v {
			n++
		}
	}
	return n
}

func expandStts(stts *mp4.Stts) ([]uint32, error) {
	var out []uint32
	for _, e := range stts.Entries {
		if e.SampleCount > 1_000_000 {
			return nil, fmt.Errorf("entry declares %d samples", e.SampleCount)
		}
		for i := uint32(0); i < e.SampleCount; i++ {
			out = append(out, e.SampleDelta)
		}
	}
	return out, nil
}

func expandCtts(ctts *mp4.Ctts, count int) ([]int64, error) {
	if ctts == nil {
		return nil, nil
	}
	out := make([]int64, 0, count)
	for _, e := range ctts.Entries {
		var off int64
		if ctts.GetVersion() == 0 {
			off = int64(e.SampleOffsetV0)
		} else {
			off = int64(e.SampleOffsetV1)
		}
		for j := uint32(0); j < e.SampleCount && len(out) < count; j++ {
			out = append(out, off)
		}
	}
	// A short ctts table is a damaged file: pad with zero offsets so the
	// remaining presentation times stay usable rather than aborting the
	// whole track over a recoverable metadata defect.
	if len(out) < count {
		out = append(out, make([]int64, count-len(out))...)
	}
	return out[:count], nil
}

func sampleSize(stsz *mp4.Stsz, i int) uint32 {
	if stsz.SampleSize != 0 {
		return stsz.SampleSize
	}
	if i < len(stsz.EntrySize) {
		return stsz.EntrySize[i]
	}
	return 0
}

// sampleOffsets walks stsc/stco, advancing each sample's payload offset by its
// size, so every sample carries the exact byte range to read.
func sampleOffsets(stsc *mp4.Stsc, stco *mp4.Stco, co64 *mp4.Co64, sizes []uint32) ([]uint64, error) {
	chunks, err := chunkOffsets(stco, co64)
	if err != nil {
		return nil, err
	}
	if stsc == nil || len(stsc.Entries) == 0 {
		return nil, fmt.Errorf("stsc is missing or empty")
	}
	count := len(sizes)
	out := make([]uint64, 0, count)
	for si, e := range stsc.Entries {
		first := int(e.FirstChunk)
		last := len(chunks)
		if si+1 < len(stsc.Entries) {
			last = int(stsc.Entries[si+1].FirstChunk) - 1
		}
		if first < 1 || first > len(chunks) {
			return nil, fmt.Errorf("stsc entry %d names chunk %d of %d", si, first, len(chunks))
		}
		if last > len(chunks) {
			last = len(chunks)
		}
		if e.SamplesPerChunk == 0 {
			return nil, fmt.Errorf("stsc entry %d declares zero samples per chunk", si)
		}
		for ci := first; ci <= last && len(out) < count; ci++ {
			off := chunks[ci-1]
			for k := uint32(0); k < e.SamplesPerChunk && len(out) < count; k++ {
				out = append(out, off)
				off += uint64(sizes[len(out)-1])
			}
		}
	}
	if len(out) < count {
		return nil, fmt.Errorf("chunk tables address %d of %d samples", len(out), count)
	}
	return out, nil
}

// chunkOffsets returns the chunk start offsets in file order.
func chunkOffsets(stco *mp4.Stco, co64 *mp4.Co64) ([]uint64, error) {
	if stco != nil {
		out := make([]uint64, len(stco.ChunkOffset))
		for i, v := range stco.ChunkOffset {
			out[i] = uint64(v)
		}
		return out, nil
	}
	if co64 != nil {
		out := make([]uint64, len(co64.ChunkOffset))
		copy(out, co64.ChunkOffset)
		return out, nil
	}
	return nil, fmt.Errorf("stco/co64 is missing")
}
