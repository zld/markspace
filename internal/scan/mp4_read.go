package scan

import (
	"encoding/binary"
	"fmt"
	"os"

	"github.com/abema/go-mp4"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
)

func buildMP4Index(idx *Index) error {
	path := idx.SourcePath
	f, err := os.Open(path)
	if err != nil {
		return fault.At(fault.Wrap(fault.IOFailure, err), path, -1)
	}
	defer f.Close()

	size := idx.SourceSize
	if err := validateTopBoxes(f, path, size); err != nil {
		return err
	}

	trakInfos, err := mp4.ExtractBox(f, nil, mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeTrak()})
	if err != nil {
		return fault.At(fault.Wrap(fault.CorruptHeader, err), path, -1)
	}
	mvhd, err := firstPayload[mp4.Mvhd](f, nil, mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeMvhd()})
	if err != nil {
		return fault.At(err, path, -1)
	}
	movieTS := uint32(1000)
	if mvhd != nil && mvhd.Timescale != 0 {
		movieTS = mvhd.Timescale
	}

	for _, trak := range trakInfos {
		if err := indexMP4Trak(f, path, idx, trak, movieTS); err != nil {
			return err
		}
	}
	return nil
}

func validateTopBoxes(f *os.File, path string, size int64) error {
	var off int64
	for off+8 <= size {
		if _, err := f.Seek(off, 0); err != nil {
			return fault.At(fault.Wrap(fault.IOFailure, err), path, off)
		}
		bi, err := mp4.ReadBoxInfo(f)
		if err != nil {
			return fault.At(fault.Wrap(fault.CorruptHeader, err), path, off)
		}
		end := off + int64(bi.Size)
		if bi.Size < bi.HeaderSize {
			return fault.At(fault.New(fault.CorruptHeader, fmt.Sprintf("box size %d < header %d", bi.Size, bi.HeaderSize)), path, off)
		}
		if end > size {
			return fault.At(fault.New(fault.TruncatedData, fmt.Sprintf("box %s ends at %d beyond file size %d", bi.Type, end, size)), path, off)
		}
		if end <= off {
			return fault.At(fault.New(fault.CorruptHeader, "non-positive box span"), path, off)
		}
		off = end
	}
	if off != size {
		return fault.At(fault.New(fault.TruncatedData, fmt.Sprintf("%d trailing byte(s) without box header", size-off)), path, off)
	}
	return nil
}

func indexMP4Trak(f *os.File, path string, idx *Index, trak *mp4.BoxInfo, movieTS uint32) error {
	tkhd, tkhdInfo, err := firstPayloadWithInfo[mp4.Tkhd](f, trak, mp4.BoxPath{mp4.BoxTypeTkhd()})
	if err != nil {
		return fault.At(err, path, int64(trak.Offset))
	}
	mdhd, mdhdInfo, err := firstPayloadWithInfo[mp4.Mdhd](f, trak, mp4.BoxPath{mp4.BoxTypeMdhd()})
	if err != nil {
		return fault.At(err, path, int64(trak.Offset))
	}
	hdlr, hdlrInfo, err := firstPayloadWithInfo[mp4.Hdlr](f, trak, mp4.BoxPath{mp4.BoxTypeHdlr()})
	if err != nil {
		return fault.At(err, path, int64(trak.Offset))
	}
	if mdhd.Timescale == 0 {
		return fault.At(fault.New(fault.CorruptHeader, "zero media timescale"), path, int64(mdhdInfo.Offset))
	}

	handler := string(hdlr.HandlerType[:])
	kind := map[string]string{"vide": "video", "soun": "audio"}[handler]
	if kind == "" {
		return fault.At(fault.New(fault.UnsupportedTrack, "unsupported handler "+handler), path, int64(hdlrInfo.Offset))
	}

	stsd, stsdInfo, err := firstPayloadWithInfo[mp4.Stsd](f, trak, mp4.BoxPath{mp4.BoxTypeStbl(), mp4.BoxTypeStsd()})
	if err != nil || stsd == nil {
		return fault.At(fault.New(fault.CorruptHeader, "missing stsd"), path, int64(trak.Offset))
	}
	codec, entryOff, err := readSampleEntryHeader(f, stsdInfo)
	if err != nil {
		return fault.At(err, path, int64(stsdInfo.Offset))
	}

	tr := Track{
		ID:            tkhd.TrackID,
		Handler:       handler,
		Kind:          kind,
		Codec:         codec,
		BoxOffset:     int64(tkhdInfo.Offset),
		Timescale:     mdhd.Timescale,
		DurationUnits: mdhd.GetDuration(),
		DurationNS:    clock.Timebase{Timescale: mdhd.Timescale}.ToNS(int64(mdhd.GetDuration())),
	}
	readAVSampleFields(f, entryOff, codec, &tr)
	if elst, elstInfo, _ := firstPayloadWithInfo[mp4.Elst](f, trak, mp4.BoxPath{mp4.BoxTypeEdts(), mp4.BoxTypeElst()}); elst != nil {
		tr.Edits, tr.InitialOffsetNS = convertEdits(elst, movieTS, mdhd.Timescale)
		_ = elstInfo
	}
	idx.Tracks = append(idx.Tracks, tr)

	stsz, _, err := firstPayloadWithInfo[mp4.Stsz](f, trak, mp4.BoxPath{mp4.BoxTypeStbl(), mp4.BoxTypeStsz()})
	if err != nil || stsz == nil || stsz.SampleCount == 0 {
		return fault.At(fault.New(fault.CorruptHeader, "missing/empty stsz"), path, int64(trak.Offset))
	}
	if int(stsz.SampleCount) > idx.Params.MaxEntries {
		return fault.New(fault.LimitExceeded, "too many samples")
	}
	stts, _, err := firstPayloadWithInfo[mp4.Stts](f, trak, mp4.BoxPath{mp4.BoxTypeStbl(), mp4.BoxTypeStts()})
	if err != nil || stts == nil {
		return fault.At(fault.New(fault.CorruptHeader, "missing stts"), path, int64(trak.Offset))
	}
	stsc, _, err := firstPayloadWithInfo[mp4.Stsc](f, trak, mp4.BoxPath{mp4.BoxTypeStbl(), mp4.BoxTypeStsc()})
	if err != nil || stsc == nil || len(stsc.Entries) == 0 {
		return fault.At(fault.New(fault.CorruptHeader, "missing stsc"), path, int64(trak.Offset))
	}
	offsets, err := readChunkOffsets(f, path, trak)
	if err != nil {
		return err
	}
	if err := appendSamples(f, idx, path, tr, trak, stsz, stts, stsc, offsets); err != nil {
		return err
	}
	return nil
}

func firstPayload[T any](f *os.File, parent *mp4.BoxInfo, path mp4.BoxPath) (*T, error) {
	v, _, err := firstPayloadWithInfo[T](f, parent, path)
	return v, err
}

func firstPayloadWithInfo[T any](f *os.File, parent *mp4.BoxInfo, path mp4.BoxPath) (*T, *mp4.BoxInfo, error) {
	boxes, err := mp4.ExtractBoxesWithPayload(f, parent, []mp4.BoxPath{path})
	if err != nil {
		return nil, nil, err
	}
	for _, bw := range boxes {
		if v, ok := any(bw.Payload).(*T); ok {
			info := bw.Info
			return v, &info, nil
		}
	}
	return nil, nil, fault.New(fault.NotFound, "box not found")
}

func readSampleEntryHeader(f *os.File, stsd *mp4.BoxInfo) (string, int64, error) {
	pos := int64(stsd.Offset) + int64(stsd.HeaderSize) + 8 // full box + entry count
	var header [8]byte
	if _, err := f.ReadAt(header[:], pos); err != nil {
		return "", 0, fault.Wrap(fault.CorruptHeader, err)
	}
	size := int64(binary.BigEndian.Uint32(header[:4]))
	if size < 8 || pos+size > int64(stsd.Offset+stsd.Size) {
		return "", pos, fault.New(fault.CorruptHeader, "invalid sample entry size")
	}
	return string(header[4:8]), pos, nil
}

func readAVSampleFields(f *os.File, entryOff int64, codec string, tr *Track) {
	switch codec {
	case "mp4a":
		var buf [14]byte
		if _, err := f.ReadAt(buf[:], entryOff+24); err == nil {
			tr.Channels = binary.BigEndian.Uint16(buf[0:2])
			tr.BitsPerSample = binary.BigEndian.Uint16(buf[2:4])
			tr.SampleRate = binary.BigEndian.Uint32(buf[10:14]) >> 16
		}
	case "mp4v", "avc1":
		var buf [4]byte
		if _, err := f.ReadAt(buf[:], entryOff+32); err == nil {
			tr.Width = binary.BigEndian.Uint16(buf[0:2])
			tr.Height = binary.BigEndian.Uint16(buf[2:4])
		}
	}
}

func convertEdits(elst *mp4.Elst, movieTS, mediaTS uint32) ([]EditMapping, int64) {
	mb := clock.Timebase{Timescale: movieTS}
	mt := clock.Timebase{Timescale: mediaTS}
	out := make([]EditMapping, 0, len(elst.Entries))
	var cursorNS, initialOffset int64
	for i, e := range elst.Entries {
		segUnits, mediaUnits := editUnits(elst.Version, &e)
		segNS := mb.ToNS(segUnits)
		mediaNS := mt.ToNS(mediaUnits)
		out = append(out, EditMapping{SegmentDurationNS: segNS, MediaTimeNS: mediaNS, MediaRate: e.MediaRateInteger})
		if i == 0 {
			initialOffset = cursorNS - mediaNS
		}
		cursorNS += segNS
	}
	return out, initialOffset
}

func editUnits(version uint8, e *mp4.ElstEntry) (seg int64, media int64) {
	if version == 1 {
		return int64(e.SegmentDurationV1), e.MediaTimeV1
	}
	return int64(e.SegmentDurationV0), int64(e.MediaTimeV0)
}

func readChunkOffsets(f *os.File, path string, trak *mp4.BoxInfo) ([]uint64, error) {
	if stco, _, err := firstPayloadWithInfo[mp4.Stco](f, trak, mp4.BoxPath{mp4.BoxTypeStbl(), mp4.BoxTypeStco()}); err == nil && stco != nil {
		out := make([]uint64, len(stco.ChunkOffset))
		for i, v := range stco.ChunkOffset {
			out[i] = uint64(v)
		}
		return out, nil
	}
	co64, _, err := firstPayloadWithInfo[mp4.Co64](f, trak, mp4.BoxPath{mp4.BoxTypeStbl(), mp4.BoxTypeCo64()})
	if err != nil || co64 == nil {
		return nil, fault.At(fault.New(fault.CorruptHeader, "missing stco/co64"), path, int64(trak.Offset))
	}
	return co64.ChunkOffset, nil
}
