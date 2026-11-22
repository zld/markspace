package index

import (
	"bytes"
	"io"

	"github.com/abema/go-mp4"

	"mediacore"
)

// SampleEntry is one parsed MP4 sample on the shared integer time base.
type SampleEntry struct {
	Index        int
	Track        mediacore.TrackID
	Presentation int64  // integer nanoseconds on the master time base
	ScaleDelta   uint32 // raw stts delta in the track's timescale units
	Size         int
	ChunkOffset  int64
	Keyframe     bool
}

// TrackInfo is the parsed, validated view of one MP4 track.
type TrackInfo struct {
	Track        mediacore.TrackID
	Timescale    uint32
	TrackID      uint32
	StartOffset  int64 // nanoseconds on the master time base
	DurationNS   int64
	Samples      []SampleEntry
	HasKeyframes bool
}

// Container is the parsed MP4 result.
type Container struct {
	Tracks []TrackInfo
}

// ParseMP4 reads a complete MP4 stream with strict bounds. Truncated boxes,
// unknown required structures, oversized samples and too many tracks return
// classified errors. The raw stream is copied so the parser never retains the
// caller's buffer.
func ParseMP4(trackFor func(trackID uint32) mediacore.TrackID, r io.Reader) (*Container, error) {
	raw, err := io.ReadAll(io.LimitReader(r, mediacore.MaxBlockBytes+1))
	if err != nil {
		return nil, mediacore.WrapError(mediacore.CatCorrupt, "", "mp4.read", 0, "unreadable MP4", err)
	}
	if len(raw) > mediacore.MaxBlockBytes {
		return nil, mediacore.NewError(mediacore.CatLimit, "", "mp4.size", int64(len(raw)),
			"MP4 exceeds MaxBlockBytes")
	}
	rs := bytes.NewReader(raw)
	// Uniform handler: containers expand into child nodes; known leaf boxes
	// are payload-only. A box that is not in the leaf set is treated as a
	// container and an expansion failure propagates as corruption.
	boxes, err := mp4.ReadBoxStructure(rs, func(h *mp4.ReadHandle) (interface{}, error) {
		n := &node{info: h.BoxInfo}
		if isLeaf(h.BoxInfo.Type) {
			p, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			n.payload = p
			return n, nil
		}
		children, eerr := h.Expand()
		if eerr != nil {
			return nil, eerr
		}
		n.children = children
		return n, nil
	})
	if err != nil {
		return nil, mediacore.WrapError(mediacore.CatCorrupt, "", "mp4.structure", 0,
			"truncated or malformed MP4 structure", err)
	}
	moov := findBox(boxes, mp4.BoxTypeMoov())
	if moov == nil {
		return nil, mediacore.NewError(mediacore.CatEmpty, "", "mp4.moov", 0, "MP4 has no moov box")
	}
	traks := findAll(moov, mp4.BoxTypeTrak())
	if len(traks) == 0 {
		return nil, mediacore.NewError(mediacore.CatEmpty, "", "mp4.trak", 0, "MP4 has no tracks")
	}
	if len(traks) > mediacore.MaxTracks {
		return nil, mediacore.NewError(mediacore.CatLimit, "", "mp4.tracks", int64(len(traks)),
			"track count exceeds MaxTracks")
	}
	c := &Container{}
	for _, trak := range traks {
		ti, err := parseTrack(trak, trackFor)
		if err != nil {
			return nil, err
		}
		c.Tracks = append(c.Tracks, ti)
	}
	return c, nil
}

func parseTrack(trak interface{}, trackFor func(uint32) mediacore.TrackID) (TrackInfo, error) {
	tkhdBox := findBox(toSlice(trak), mp4.BoxTypeTkhd())
	mdia := findBox(toSlice(trak), mp4.BoxTypeMdia())
	if tkhdBox == nil || mdia == nil {
		return TrackInfo{}, mediacore.NewError(mediacore.CatCorrupt, "", "mp4.trak", 0,
			"track missing tkhd or mdia")
	}
	tkhd, _ := payload(tkhdBox).(*mp4.Tkhd)
	mdhdBox := findBox(toSlice(mdia), mp4.BoxTypeMdhd())
	if tkhd == nil || mdhdBox == nil {
		return TrackInfo{}, mediacore.NewError(mediacore.CatCorrupt, "", "mp4.trak", 0,
			"track missing tkhd/mdhd payload")
	}
	mdhd, _ := payload(mdhdBox).(*mp4.Mdhd)
	if mdhd == nil || mdhd.Timescale == 0 {
		return TrackInfo{}, mediacore.NewError(mediacore.CatCorrupt, "", "mp4.mdhd", 0,
			"invalid media header timescale")
	}
	minf := findBox(toSlice(mdia), mp4.BoxTypeMinf())
	if minf == nil {
		return TrackInfo{}, mediacore.NewError(mediacore.CatCorrupt, "", "mp4.minf", 0,
			"track missing minf")
	}
	stbl := findBox(toSlice(minf), mp4.BoxTypeStbl())
	if stbl == nil {
		return TrackInfo{}, mediacore.NewError(mediacore.CatCorrupt, "", "mp4.stbl", 0,
			"track missing sample table")
	}
	tid := trackFor(tkhd.TrackID)
	dur := scaleToNS(mdhdDuration(mdhd), mdhd.Timescale)
	ti := TrackInfo{Track: tid, Timescale: mdhd.Timescale, TrackID: tkhd.TrackID,
		StartOffset: scaleToNS(tkhdDuration(tkhd), 1000), DurationNS: dur}
	samples, keyed, err := buildSamples(stbl, mdhd.Timescale, tid)
	if err != nil {
		return TrackInfo{}, err
	}
	ti.Samples, ti.HasKeyframes = samples, keyed
	if len(ti.Samples) == 0 {
		return TrackInfo{}, mediacore.NewError(mediacore.CatEmpty, tid, "mp4.samples", 0,
			"empty track sample table")
	}
	return ti, nil
}

func buildSamples(stbl interface{}, scale uint32, tid mediacore.TrackID) ([]SampleEntry, bool, error) {
	stts := payload(findBox(toSlice(stbl), mp4.BoxTypeStts()))
	stsz := payload(findBox(toSlice(stbl), mp4.BoxTypeStsz()))
	stsc := payload(findBox(toSlice(stbl), mp4.BoxTypeStsc()))
	stco := payload(findBox(toSlice(stbl), mp4.BoxTypeStco()))
	co64 := payload(findBox(toSlice(stbl), mp4.BoxTypeCo64()))
	stss := payload(findBox(toSlice(stbl), mp4.BoxTypeStss()))
	if stts == nil || stsz == nil || stsc == nil || (stco == nil && co64 == nil) {
		return nil, false, mediacore.NewError(mediacore.CatCorrupt, tid, "mp4.stbl", 0,
			"sample table missing stts/stsz/stsc/stco")
	}
	deltas := expandDeltas(stts.(*mp4.Stts))
	sizes := expandSizes(stsz.(*mp4.Stsz))
	if len(deltas) == 0 || len(deltas) != len(sizes) {
		return nil, false, mediacore.NewError(mediacore.CatCorrupt, tid, "mp4.stbl", 0,
			"stts/stsz sample count mismatch")
	}
	if len(deltas) > mediacore.MaxSegments {
		return nil, false, mediacore.NewError(mediacore.CatLimit, tid, "mp4.samples", int64(len(deltas)),
			"sample count exceeds bounded limit")
	}
	offsets, err := expandOffsets(stsc.(*mp4.Stsc), stco, co64, sizes)
	if err != nil {
		return nil, false, err
	}
	if len(offsets) != len(sizes) {
		return nil, false, mediacore.NewError(mediacore.CatCorrupt, tid, "mp4.chunks", 0,
			"chunk offset table does not cover all samples")
	}
	keyframes := map[uint32]struct{}{}
	if stss != nil {
		for _, n := range stss.(*mp4.Stss).SampleNumber {
			keyframes[n] = struct{}{}
		}
	}
	out := make([]SampleEntry, len(deltas))
	var tscaled uint64
	for i := range deltas {
		if sizes[i] > mediacore.MaxBlockBytes {
			return nil, false, mediacore.NewError(mediacore.CatLimit, tid, "mp4.sample",
				int64(offsets[i]), "sample exceeds MaxBlockBytes")
		}
		out[i] = SampleEntry{
			Index: i, Track: tid, Presentation: scaleToNS(tscaled, scale),
			ScaleDelta: deltas[i], Size: int(sizes[i]),
			ChunkOffset: int64(offsets[i]),
			Keyframe:    stss == nil || contains(keyframes, uint32(i+1)),
		}
		tscaled += uint64(deltas[i])
	}
	return out, stss != nil, nil
}

func expandDeltas(stts *mp4.Stts) []uint32 {
	n := 0
	for _, e := range stts.Entries {
		n += int(e.SampleCount)
	}
	out := make([]uint32, 0, n)
	for _, e := range stts.Entries {
		for i := uint32(0); i < e.SampleCount; i++ {
			out = append(out, e.SampleDelta)
		}
	}
	return out
}

func expandSizes(stsz *mp4.Stsz) []uint32 {
	if stsz.SampleSize != 0 {
		out := make([]uint32, stsz.SampleCount)
		for i := range out {
			out[i] = stsz.SampleSize
		}
		return out
	}
	return append([]uint32(nil), stsz.EntrySize...)
}

func expandOffsets(stsc *mp4.Stsc, stco, co64 interface{}, sizes []uint32) ([]uint64, error) {
	var chunks []uint64
	if stco != nil {
		for _, o := range stco.(*mp4.Stco).ChunkOffset {
			chunks = append(chunks, uint64(o))
		}
	} else {
		chunks = append(chunks, co64.(*mp4.Co64).ChunkOffset...)
	}
	if len(stsc.Entries) == 0 {
		return nil, mediacore.NewError(mediacore.CatCorrupt, "", "mp4.stsc", 0, "empty stsc entries")
	}
	out := make([]uint64, 0, len(sizes))
	sampleIdx := 0
	for ci := 0; ci < len(chunks); ci++ {
		perChunk := samplesPerChunk(stsc.Entries, uint32(ci)+1)
		off := chunks[ci]
		for k := uint32(0); k < perChunk; k++ {
			if sampleIdx >= len(sizes) {
				return nil, mediacore.NewError(mediacore.CatCorrupt, "", "mp4.stsc", int64(ci),
					"stsc references more samples than stsz declares")
			}
			out = append(out, off)
			off += uint64(sizes[sampleIdx])
			sampleIdx++
		}
	}
	return out, nil
}

func samplesPerChunk(entries []mp4.StscEntry, chunk uint32) uint32 {
	per := entries[0].SamplesPerChunk
	for _, e := range entries {
		if chunk >= e.FirstChunk {
			per = e.SamplesPerChunk
		}
	}
	return per
}

func scaleToNS(ticks uint64, scale uint32) int64 {
	if scale == 0 {
		return 0
	}
	return int64(ticks * 1_000_000_000 / uint64(scale))
}

func contains(m map[uint32]struct{}, k uint32) bool {
	_, ok := m[k]
	return ok
}
