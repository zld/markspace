package scan

import (
	"sort"

	"github.com/abema/go-mp4"

	"archivist/internal/fault"
)

// decodeTables reads mvhd and every trak's sample tables. All timestamps are
// kept as their on-disk 32-bit presentation values; expanding them onto a
// monotonic timeline is the repairer's job.
func (r *Reader) decodeTables(c *Container) error {
	mvhdRef, ok := c.findFirst("mvhd", 1)
	if !ok {
		return fault.Corrupt(fault.StageScan, r.path, 0, "mvhd", "movie header missing")
	}
	var mvhd mp4.Mvhd
	if err := r.unmarshal(mvhdRef, &mvhd); err != nil {
		return err
	}
	if mvhd.Timescale == 0 {
		return fault.Corrupt(fault.StageScan, r.path, mvhdRef.Offset, "mvhd", "movie timescale is zero")
	}
	c.MovieTimescale = mvhd.Timescale

	trakRefs := c.findAll("trak")
	if len(trakRefs) == 0 {
		return fault.Corrupt(fault.StageScan, r.path, mvhdRef.Offset, "moov", "no tracks found")
	}
	for _, trak := range trakRefs {
		tr, err := r.decodeTrack(c, trak)
		if err != nil {
			return err
		}
		c.Tracks = append(c.Tracks, tr)
	}
	return nil
}

// children returns boxes directly inside parent.
func (c *Container) children(parent BoxRef) []BoxRef {
	var out []BoxRef
	for _, b := range c.Boxes {
		if b.Depth == parent.Depth+1 && b.Offset > parent.Offset && b.Offset < parent.End() {
			out = append(out, b)
		}
	}
	return out
}

func (c *Container) child(parent BoxRef, typ string) (BoxRef, bool) {
	for _, b := range c.children(parent) {
		if b.Type == typ {
			return b, true
		}
	}
	return BoxRef{}, false
}

func (r *Reader) decodeTrack(c *Container, trak BoxRef) (*Track, error) {
	tr := &Track{}
	tkhdRef, ok := c.child(trak, "tkhd")
	if !ok {
		return nil, fault.Corrupt(fault.StageScan, r.path, trak.Offset, "trak", "track header missing")
	}
	var tkhd mp4.Tkhd
	if err := r.unmarshal(tkhdRef, &tkhd); err != nil {
		return nil, err
	}
	tr.ID = tkhd.TrackID
	tr.Width = tkhd.Width >> 16
	tr.Height = tkhd.Height >> 16

	mdiaRef, ok := c.child(trak, "mdia")
	if !ok {
		return nil, fault.Corrupt(fault.StageScan, r.path, trak.Offset, "trak", "media box missing")
	}
	mdhdRef, ok := c.child(mdiaRef, "mdhd")
	if !ok {
		return nil, fault.Corrupt(fault.StageScan, r.path, mdiaRef.Offset, "mdia", "media header missing")
	}
	var mdhd mp4.Mdhd
	if err := r.unmarshal(mdhdRef, &mdhd); err != nil {
		return nil, err
	}
	if mdhd.Timescale == 0 {
		return nil, fault.Corrupt(fault.StageScan, r.path, mdhdRef.Offset, "mdhd", "track timescale is zero")
	}
	tr.Timescale = mdhd.Timescale
	tr.Duration = mdhd.GetDuration()

	hdlrRef, ok := c.child(mdiaRef, "hdlr")
	if !ok {
		return nil, fault.Corrupt(fault.StageScan, r.path, mdiaRef.Offset, "mdia", "handler box missing")
	}
	var hdlr mp4.Hdlr
	if err := r.unmarshal(hdlrRef, &hdlr); err != nil {
		return nil, err
	}
	tr.Handler = string(hdlr.HandlerType[:])

	minfRef, ok := c.child(mdiaRef, "minf")
	if !ok {
		return nil, fault.Corrupt(fault.StageScan, r.path, mdiaRef.Offset, "mdia", "media information box missing")
	}
	stblRef, ok := c.child(minfRef, "stbl")
	if !ok {
		return nil, fault.Corrupt(fault.StageScan, r.path, minfRef.Offset, "minf", "sample table box missing")
	}
	if err := r.decodeSampleTable(c, stblRef, tr); err != nil {
		return nil, err
	}
	return tr, nil
}

// decodeSampleTable converts stts/ctts/stss/stsz/stsc/stco into samples.
func (r *Reader) decodeSampleTable(c *Container, stbl BoxRef, tr *Track) error {
	var stts mp4.Stts
	sttsRef, ok := c.child(stbl, "stts")
	if !ok {
		return fault.Corrupt(fault.StageScan, r.path, stbl.Offset, "stbl", "decode time table missing")
	}
	if err := r.unmarshal(sttsRef, &stts); err != nil {
		return err
	}
	var ctts mp4.Ctts
	cttsRef, hasCTTS := c.child(stbl, "ctts")
	if hasCTTS {
		if err := r.unmarshal(cttsRef, &ctts); err != nil {
			return err
		}
	}
	var stsz mp4.Stsz
	stszRef, ok := c.child(stbl, "stsz")
	if !ok {
		return fault.Corrupt(fault.StageScan, r.path, stbl.Offset, "stbl", "sample size table missing")
	}
	if err := r.unmarshal(stszRef, &stsz); err != nil {
		return err
	}
	var chunkOffsets []uint64
	if stcoRef, ok := c.child(stbl, "stco"); ok {
		var stco mp4.Stco
		if err := r.unmarshal(stcoRef, &stco); err != nil {
			return err
		}
		for _, off := range stco.ChunkOffset {
			chunkOffsets = append(chunkOffsets, uint64(off))
		}
	} else if co64Ref, ok := c.child(stbl, "co64"); ok {
		var co64 mp4.Co64
		if err := r.unmarshal(co64Ref, &co64); err != nil {
			return err
		}
		chunkOffsets = append(chunkOffsets, co64.ChunkOffset...)
	} else {
		return fault.Corrupt(fault.StageScan, r.path, stbl.Offset, "stbl", "chunk offset table missing")
	}
	var stsc mp4.Stsc
	stscRef, ok := c.child(stbl, "stsc")
	if !ok {
		return fault.Corrupt(fault.StageScan, r.path, stbl.Offset, "stbl", "sample-to-chunk table missing")
	}
	if err := r.unmarshal(stscRef, &stsc); err != nil {
		return err
	}

	keyframes := map[uint32]bool(nil)
	if stssRef, ok := c.child(stbl, "stss"); ok {
		var stss mp4.Stss
		if err := r.unmarshal(stssRef, &stss); err != nil {
			return err
		}
		keyframes = make(map[uint32]bool, len(stss.SampleNumber))
		for _, n := range stss.SampleNumber {
			keyframes[n] = true
		}
	}

	// Expand stts into per-sample decode timestamps.
	var dts []int64
	var cur int64
	for _, e := range stts.Entries {
		for i := uint32(0); i < e.SampleCount; i++ {
			dts = append(dts, cur)
			cur += int64(e.SampleDelta)
		}
	}
	sizes := sampleSizes(stsz)
	if len(dts) != len(sizes) {
		return fault.Corrupt(fault.StageScan, r.path, stszRef.Offset, "stsz",
			"decode time entries (%d) disagree with sample count (%d)", len(dts), len(sizes))
	}

	// Expand ctts into composition offsets; missing entries mean zero.
	// Version 1 carries a signed offset, version 0 an unsigned one. Both are
	// added modulo 2^32, which is exactly how a reader reconstructs PTS.
	cttsV1 := hasCTTS && ctts.GetVersion() == 1
	offsets := make([]uint32, len(dts))
	var idx int
	for _, e := range ctts.Entries {
		var off uint32
		if cttsV1 {
			off = uint32(e.SampleOffsetV1)
		} else {
			off = e.SampleOffsetV0
		}
		for i := uint32(0); i < e.SampleCount && idx < len(offsets); i++ {
			offsets[idx] = off
			idx++
		}
	}

	sampleOffsets, err := r.sampleOffsets(stsc, stscRef, chunkOffsets, sizes, c)
	if err != nil {
		return err
	}

	for i := range dts {
		// The stored presentation timestamp is a 32-bit pattern; wrap the
		// composed value back into [0,2^32).
		rawPTS := uint32(uint64(dts[i]) + uint64(offsets[i]))
		s := Sample{
			Index:    i + 1,
			DTS:      dts[i],
			PTS:      int64(rawPTS),
			Size:     sizes[i],
			Offset:   sampleOffsets[i],
			Keyframe: keyframes == nil || keyframes[uint32(i+1)],
		}
		tr.Samples = append(tr.Samples, s)
	}
	return nil
}

func sampleSizes(stsz mp4.Stsz) []uint32 {
	if stsz.SampleSize != 0 {
		out := make([]uint32, stsz.SampleCount)
		for i := range out {
			out[i] = stsz.SampleSize
		}
		return out
	}
	return stsz.EntrySize
}

// sampleOffsets maps each sample to its absolute byte offset in the file.
func (r *Reader) sampleOffsets(stsc mp4.Stsc, ref BoxRef, chunks []uint64, sizes []uint32, c *Container) ([]int64, error) {
	if len(stsc.Entries) == 0 {
		if len(sizes) == 0 {
			return nil, nil
		}
		return nil, fault.Corrupt(fault.StageScan, r.path, ref.Offset, "stsc", "sample-to-chunk table is empty")
	}
	entries := append([]mp4.StscEntry(nil), stsc.Entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].FirstChunk < entries[j].FirstChunk })

	out := make([]int64, len(sizes))
	var sample int
	entry := 0
	for chunkIdx := 0; chunkIdx < len(chunks); chunkIdx++ {
		chunkNo := uint32(chunkIdx + 1)
		for entry+1 < len(entries) && entries[entry+1].FirstChunk <= chunkNo {
			entry++
		}
		if chunkNo < entries[entry].FirstChunk {
			return nil, fault.Corrupt(fault.StageScan, r.path, ref.Offset, "stsc",
				"chunk %d precedes first sample-to-chunk entry", chunkNo)
		}
		perChunk := int(entries[entry].SamplesPerChunk)
		off := chunks[chunkIdx]
		if off+uint64(mdatSpan(sizes[sample:min(sample+perChunk, len(sizes))])) > uint64(c.Size) {
			return nil, fault.Corrupt(fault.StageScan, r.path, int64(off), "stco",
				"chunk offset %d plus payload exceeds file size %d", off, c.Size)
		}
		for j := 0; j < perChunk && sample < len(sizes); j++ {
			out[sample] = int64(off)
			off += uint64(sizes[sample])
			sample++
		}
	}
	if sample != len(sizes) {
		return nil, fault.Corrupt(fault.StageScan, r.path, ref.Offset, "stsc",
			"chunk tables cover %d of %d samples", sample, len(sizes))
	}
	return out, nil
}

func mdatSpan(sizes []uint32) int64 {
	var total int64
	for _, s := range sizes {
		total += int64(s)
	}
	return total
}
