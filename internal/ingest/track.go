package ingest

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/abema/go-mp4"

	"replaycheck/internal/model"
)

// ParseContainer opens and parses an MP4 source, returning the container and
// its tracks. The file handle is not retained: all sample tables are expanded
// while streaming so later stages never touch the source again.
func ParseContainer(path string, lim Limits) (*Container, error) {
	f, c, err := openContainer(path, lim)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ref := model.TrackRef{Name: filepath.Base(path)}

	root := c.Tree
	ftyp := root.Find("ftyp")
	if ftyp == nil {
		return nil, model.NewError(model.StageIngest, "ftyp", ref, 0, "missing ftyp box")
	}
	var ftypBox mp4.Ftyp
	if err := readPayload(f, ftyp, &ftypBox); err != nil {
		return nil, model.WrapError(model.StageIngest, "ftyp", ref, ftyp.Offset, "unreadable ftyp", err)
	}
	c.MajorBrand = string(ftypBox.MajorBrand[:])

	moov := root.Find("moov")
	if moov == nil {
		return nil, model.NewError(model.StageIngest, "moov", ref, 0, "missing moov box")
	}
	mvhdBox := moov.Find("mvhd")
	if mvhdBox == nil {
		return nil, model.NewError(model.StageIngest, "mvhd", ref, moov.Offset, "missing mvhd box")
	}
	var mvhd mp4.Mvhd
	if err := readPayload(f, mvhdBox, &mvhd); err != nil {
		return nil, model.WrapError(model.StageIngest, "mvhd", ref, mvhdBox.Offset, "unreadable mvhd", err)
	}
	c.Timescale = mvhd.Timescale
	c.Duration = mvhd.GetDuration()
	if c.Timescale == 0 {
		return nil, model.NewError(model.StageIngest, "mvhd", ref, mvhdBox.Offset, "movie timescale is zero")
	}

	for _, mdat := range root.All("mdat") {
		c.Mdat = append(c.Mdat, ByteRange{Offset: mdat.PayloadOffset(), Size: mdat.PayloadSize()})
	}

	traks := moov.All("trak")
	if len(traks) == 0 {
		return nil, model.NewError(model.StageIngest, "trak", ref, moov.Offset, "no tracks in moov")
	}
	for _, trak := range traks {
		t, err := parseTrack(f, c, trak, lim)
		if err != nil {
			return nil, err
		}
		c.Tracks = append(c.Tracks, t)
	}
	if len(c.Tracks) == 0 {
		return nil, model.NewError(model.StageIngest, "trak", ref, moov.Offset, "no usable tracks")
	}
	return c, nil
}

// readSampleEntryType reads the four-character format of the first sample
// entry. stsd is a full box followed by entry_count and the entries; the entry
// type sits 8 bytes after the payload start, inside the entry's own header.
func readSampleEntryType(r io.ReadSeeker, stsd *Box) (string, error) {
	if stsd.PayloadSize() < 16 {
		return "", fmt.Errorf("stsd payload too small: %d", stsd.PayloadSize())
	}
	buf := make([]byte, 8)
	if _, err := r.Seek(int64(stsd.PayloadOffset()+8), io.SeekStart); err != nil {
		return "", err
	}
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf[4:8]), nil
}

// readPayload unmarshals a box payload into dst.
func readPayload(r io.ReadSeeker, box *Box, dst mp4.IBox) error {
	if _, err := r.Seek(int64(box.PayloadOffset()), io.SeekStart); err != nil {
		return err
	}
	_, err := mp4.Unmarshal(r, box.PayloadSize(), dst, mp4.Context{})
	return err
}

func parseTrack(f io.ReadSeeker, c *Container, trak *Box, lim Limits) (*Track, error) {
	ref := model.TrackRef{Kind: "trak", Name: "trak"}
	t := &Track{TrakOffset: trak.Offset}

	tkhdBox := trak.Find("tkhd")
	if tkhdBox == nil {
		return nil, model.NewError(model.StageIngest, "tkhd", ref, trak.Offset, "track missing tkhd")
	}
	var tkhd mp4.Tkhd
	if err := readPayload(f, tkhdBox, &tkhd); err != nil {
		return nil, model.WrapError(model.StageIngest, "tkhd", ref, tkhdBox.Offset, "unreadable tkhd", err)
	}
	t.ID = tkhd.TrackID
	ref.ID = t.ID

	mdia := trak.Find("mdia")
	if mdia == nil {
		return nil, model.NewError(model.StageIngest, "mdia", ref, trak.Offset, "track missing mdia")
	}
	mdhdBox := mdia.Find("mdhd")
	if mdhdBox == nil {
		return nil, model.NewError(model.StageIngest, "mdhd", ref, mdia.Offset, "track missing mdhd")
	}
	var mdhd mp4.Mdhd
	if err := readPayload(f, mdhdBox, &mdhd); err != nil {
		return nil, model.WrapError(model.StageIngest, "mdhd", ref, mdhdBox.Offset, "unreadable mdhd", err)
	}
	t.MdhdOffset = mdhdBox.Offset
	t.Timescale = mdhd.Timescale
	t.Duration = mdhd.GetDuration()
	if t.Timescale == 0 {
		return nil, model.NewError(model.StageIngest, "mdhd", ref, mdhdBox.Offset, "track timescale is zero")
	}
	if t.Duration == 0 {
		return nil, model.NewError(model.StageIngest, "mdhd", ref, mdhdBox.Offset, "track duration is zero")
	}

	hdlrBox := mdia.Find("hdlr")
	if hdlrBox == nil {
		return nil, model.NewError(model.StageIngest, "hdlr", ref, mdia.Offset, "track missing hdlr")
	}
	var hdlr mp4.Hdlr
	if err := readPayload(f, hdlrBox, &hdlr); err != nil {
		return nil, model.WrapError(model.StageIngest, "hdlr", ref, hdlrBox.Offset, "unreadable hdlr", err)
	}
	t.HdlrOffset = hdlrBox.Offset
	t.Handler = string(hdlr.HandlerType[:])
	switch t.Handler {
	case "vide":
		t.Kind = "video"
	case "soun":
		t.Kind = "audio"
	default:
		t.Kind = "other"
	}

	minf := mdia.Find("minf")
	if minf == nil {
		return nil, model.NewError(model.StageIngest, "minf", ref, mdia.Offset, "track missing minf")
	}
	stbl := minf.Find("stbl")
	if stbl == nil {
		return nil, model.NewError(model.StageIngest, "stbl", ref, minf.Offset, "track missing stbl")
	}
	// Mandatory sample tables: a missing index makes sample offsets
	// unknowable and must fail here, not silently yield empty media.
	stsdBox := stbl.Find("stsd")
	sttsBox := stbl.Find("stts")
	stszBox := stbl.Find("stsz")
	stscBox := stbl.Find("stsc")
	stcoBox := stbl.Find("stco")
	co64Box := stbl.Find("co64")
	if stsdBox == nil {
		return nil, model.NewError(model.StageIngest, "stsd", ref, stbl.Offset, "missing sample description index")
	}
	if sttsBox == nil {
		return nil, model.NewError(model.StageIngest, "stts", ref, stbl.Offset, "missing time-to-sample index")
	}
	if stszBox == nil {
		return nil, model.NewError(model.StageIngest, "stsz", ref, stbl.Offset, "missing sample size index")
	}
	if stscBox == nil {
		return nil, model.NewError(model.StageIngest, "stsc", ref, stbl.Offset, "missing sample-to-chunk index")
	}
	if stcoBox == nil && co64Box == nil {
		return nil, model.NewError(model.StageIngest, "stco", ref, stbl.Offset, "missing chunk offset index")
	}
	t.StsdOffset = stsdBox.Offset
	t.SttsOffset = sttsBox.Offset
	t.StszOffset = stszBox.Offset
	t.StscOffset = stscBox.Offset

	var stsd mp4.Stsd
	if err := readPayload(f, stsdBox, &stsd); err != nil {
		return nil, model.WrapError(model.StageIngest, "stsd", ref, stsdBox.Offset, "unreadable stsd", err)
	}
	if stsd.EntryCount == 0 {
		return nil, model.NewError(model.StageIngest, "stsd", ref, stsdBox.Offset, "no sample description entries")
	}
	codec, err := readSampleEntryType(f, stsdBox)
	if err != nil {
		return nil, model.WrapError(model.StageIngest, "stsd", ref, stsdBox.Offset, "unreadable sample description", err)
	}
	t.Codec = codec

	var stts mp4.Stts
	var stsz mp4.Stsz
	var stsc mp4.Stsc
	if err := readPayload(f, sttsBox, &stts); err != nil {
		return nil, model.WrapError(model.StageIngest, "stts", ref, sttsBox.Offset, "unreadable stts", err)
	}
	if err := readPayload(f, stszBox, &stsz); err != nil {
		return nil, model.WrapError(model.StageIngest, "stsz", ref, stszBox.Offset, "unreadable stsz", err)
	}
	if err := readPayload(f, stscBox, &stsc); err != nil {
		return nil, model.WrapError(model.StageIngest, "stsc", ref, stscBox.Offset, "unreadable stsc", err)
	}
	if int(stsz.SampleCount) > lim.MaxSamples {
		return nil, model.Errorf(model.StageIngest, "stsz", ref, stszBox.Offset, "sample count %d exceeds budget %d", stsz.SampleCount, lim.MaxSamples)
	}
	offsets := make([]uint64, 0)
	if stcoBox != nil {
		var stco mp4.Stco
		if err := readPayload(f, stcoBox, &stco); err != nil {
			return nil, model.WrapError(model.StageIngest, "stco", ref, stcoBox.Offset, "unreadable stco", err)
		}
		t.StcoOffset = stcoBox.Offset
		for _, o := range stco.ChunkOffset {
			offsets = append(offsets, uint64(o))
		}
	} else {
		var co64 mp4.Co64
		if err := readPayload(f, co64Box, &co64); err != nil {
			return nil, model.WrapError(model.StageIngest, "co64", ref, co64Box.Offset, "unreadable co64", err)
		}
		t.StcoOffset = co64Box.Offset
		offsets = append(offsets, co64.ChunkOffset...)
	}

	samples, err := buildSamples(ref, t, &stts, &stsz, &stsc, offsets, c.Mdat, lim)
	if err != nil {
		return nil, err
	}
	t.Samples = samples
	t.SampleCount = len(samples)
	for _, s := range samples {
		t.MediaBytes += uint64(s.Size)
	}
	return t, nil
}

// buildSamples expands the three linked sample tables into flat samples,
// stopping before any budget or bounds violation.
func buildSamples(ref model.TrackRef, t *Track, stts *mp4.Stts, stsz *mp4.Stsz, stsc *mp4.Stsc, chunkOffsets []uint64, mdats []ByteRange, lim Limits) ([]Sample, error) {
	if len(stts.Entries) == 0 {
		return nil, model.NewError(model.StageIngest, "stts", ref, t.SttsOffset, "empty time-to-sample table")
	}
	if len(stsc.Entries) == 0 {
		return nil, model.NewError(model.StageIngest, "stsc", ref, t.StscOffset, "empty sample-to-chunk table")
	}
	if len(chunkOffsets) == 0 {
		return nil, model.NewError(model.StageIngest, "stco", ref, t.StcoOffset, "empty chunk offset table")
	}
	total := uint64(0)
	for _, e := range stts.Entries {
		total += uint64(e.SampleCount)
	}
	if stsz.SampleSize == 0 && uint64(len(stsz.EntrySize)) != uint64(stsz.SampleCount) {
		return nil, model.Errorf(model.StageIngest, "stsz", ref, t.StszOffset, "sample size table has %d entries for %d samples", len(stsz.EntrySize), stsz.SampleCount)
	}
	if stsz.SampleSize != 0 {
		total = uint64(stsz.SampleCount)
	}
	if total == 0 {
		return nil, model.NewError(model.StageIngest, "stsz", ref, t.StszOffset, "zero samples")
	}
	if total > uint64(lim.MaxSamples) {
		return nil, model.Errorf(model.StageIngest, "stsz", ref, t.StszOffset, "sample count %d exceeds budget %d", total, lim.MaxSamples)
	}

	// Per-chunk sample counts from stsc. Entry i applies from FirstChunk
	// until the chunk before entry i+1.
	samplesPerChunk := make([]uint32, len(chunkOffsets))
	for i := range samplesPerChunk {
		samplesPerChunk[i] = 1
	}
	for i, e := range stsc.Entries {
		if e.FirstChunk == 0 || int(e.FirstChunk) > len(chunkOffsets) {
			return nil, model.Errorf(model.StageIngest, "stsc", ref, t.StscOffset, "entry %d first chunk %d out of range", i, e.FirstChunk)
		}
		start := int(e.FirstChunk) - 1
		end := len(chunkOffsets)
		if i+1 < len(stsc.Entries) {
			end = int(stsc.Entries[i+1].FirstChunk) - 1
			if end < start {
				return nil, model.Errorf(model.StageIngest, "stsc", ref, t.StscOffset, "entry %d decreases first chunk", i+1)
			}
		}
		for c := start; c < end; c++ {
			samplesPerChunk[c] = e.SamplesPerChunk
		}
	}

	samples := make([]Sample, 0, total)
	var dts int64
	si := uint64(0)
	sizeAt := func(i uint64) (uint32, error) {
		if stsz.SampleSize != 0 {
			return stsz.SampleSize, nil
		}
		if i >= uint64(len(stsz.EntrySize)) {
			return 0, model.Errorf(model.StageIngest, "stsz", ref, t.StszOffset, "sample %d beyond size table", i)
		}
		return stsz.EntrySize[i], nil
	}
	for ci, base := range chunkOffsets {
		off := base
		for k := uint32(0); k < samplesPerChunk[ci]; k++ {
			if si >= total {
				return nil, model.Errorf(model.StageIngest, "stsc", ref, t.StscOffset, "sample-to-chunk overproduces sample %d", si)
			}
			dur, err := sttsDuration(stts, si, ref, t.SttsOffset)
			if err != nil {
				return nil, err
			}
			sz, err := sizeAt(si)
			if err != nil {
				return nil, err
			}
			if !inMdat(off, uint64(sz), mdats) {
				return nil, model.Errorf(model.StageIngest, "chunk", ref, off, "sample %d range %d+%d outside mdat", si, off, sz)
			}
			samples = append(samples, Sample{DTS: dts, Size: sz, Offset: off})
			off += uint64(sz)
			dts += int64(dur)
			si++
			if si > uint64(lim.MaxSamples) {
				return nil, model.Errorf(model.StageIngest, "stsz", ref, t.StszOffset, "sample expansion exceeds budget")
			}
		}
	}
	if si != total {
		return nil, model.Errorf(model.StageIngest, "stsc", ref, t.StscOffset, "sample tables disagree: chunks produce %d samples, stsz declares %d", si, total)
	}
	return samples, nil
}

func inMdat(off, size uint64, mdats []ByteRange) bool {
	for _, m := range mdats {
		if off >= m.Offset && off+size <= m.Offset+m.Size {
			return true
		}
	}
	return false
}

func sttsDuration(stts *mp4.Stts, sample uint64, ref model.TrackRef, offset uint64) (uint32, error) {
	seen := uint64(0)
	for _, e := range stts.Entries {
		if sample < seen+uint64(e.SampleCount) {
			return e.SampleDelta, nil
		}
		seen += uint64(e.SampleCount)
	}
	return 0, model.Errorf(model.StageIngest, "stts", ref, offset, "sample %d beyond time table", sample)
}
