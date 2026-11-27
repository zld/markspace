package ingest

import (
	"bytes"
	"encoding/binary"

	mp4 "github.com/abema/go-mp4"

	merr "medialib/internal/errors"
)

// MP4Info is the validated index view of one audio track.
type MP4Info struct {
	Timescale     uint32
	DurationTicks uint64
	Channels      uint16
	SampleRate    uint32
	SampleCount   uint32
	SampleSizes   []uint32
	ChunkOffsets  []uint32
	MdatOffset    uint64 // payload offset of mdat
	MdatSize      uint64 // payload size of mdat
	FileSize      int64
}

// ParseMP4 validates box boundaries, the track time base and every
// index reference. Truncated, out-of-range or corrupt input yields an
// *errors.Error carrying the parse/verify stage and the byte offset of
// the offending box; the timeline is never silently shortened.
func ParseMP4(data []byte) (*MP4Info, error) {
	if err := checkTopLevelBounds(data); err != nil {
		return nil, err
	}
	info := &MP4Info{FileSize: int64(len(data))}
	r := bytes.NewReader(data)
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		off := int64(h.BoxInfo.Offset)
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMdhd():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, merr.New(merr.StageParse, off, "mdhd", e)
			}
			m := box.(*mp4.Mdhd)
			info.Timescale = m.Timescale
			info.DurationTicks = m.GetDuration()
		case mp4.BoxTypeMp4a():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, merr.New(merr.StageParse, off, "mp4a", e)
			}
			a := box.(*mp4.AudioSampleEntry)
			info.Channels = a.ChannelCount
			info.SampleRate = a.SampleRate >> 16
		case mp4.BoxTypeStsz():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, merr.New(merr.StageParse, off, "stsz", e)
			}
			s := box.(*mp4.Stsz)
			info.SampleCount = s.SampleCount
			info.SampleSizes = s.EntrySize
		case mp4.BoxTypeStco():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, merr.New(merr.StageParse, off, "stco", e)
			}
			info.ChunkOffsets = box.(*mp4.Stco).ChunkOffset
		case mp4.BoxTypeStts():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, merr.New(merr.StageParse, off, "stts", e)
			}
			stts := box.(*mp4.Stts)
			var n uint64
			for _, ent := range stts.Entries {
				n += uint64(ent.SampleCount)
			}
			if info.SampleCount != 0 && n != uint64(info.SampleCount) {
				return nil, merr.Newf(merr.StageVerify, off, "stts",
					"stts sample count %d != stsz count %d", n, info.SampleCount)
			}
		case mp4.BoxTypeMdat():
			info.MdatOffset = h.BoxInfo.Offset + h.BoxInfo.HeaderSize
			info.MdatSize = h.BoxInfo.Size - h.BoxInfo.HeaderSize
			return nil, nil // never read the payload
		}
		if h.BoxInfo.IsSupportedType() {
			return h.Expand()
		}
		return nil, nil
	})
	if err != nil {
		if _, ok := merr.StageOf(err); ok {
			return nil, err
		}
		return nil, merr.New(merr.StageParse, -1, "walk", err)
	}
	return info, verifyIndex(info)
}

// checkTopLevelBounds walks top-level box headers and rejects any box
// whose declared extent exceeds the input, without reading past it.
func checkTopLevelBounds(data []byte) error {
	size := int64(len(data))
	for off := int64(0); off < size; {
		if size-off < 8 {
			return merr.Newf(merr.StageParse, off, "box-header",
				"truncated box header: %d bytes remain", size-off)
		}
		boxSize := int64(binary.BigEndian.Uint32(data[off:]))
		typ := string(data[off+4 : off+8])
		hdr := int64(8)
		if boxSize == 1 {
			if size-off < 16 {
				return merr.Newf(merr.StageParse, off, "box-header", "truncated largesize header")
			}
			boxSize = int64(binary.BigEndian.Uint64(data[off+8:]))
			hdr = 16
		} else if boxSize == 0 {
			boxSize = size - off // extends to EOF; only valid as last box
		}
		if boxSize < hdr {
			return merr.Newf(merr.StageParse, off, "box-header",
				"invalid box size %d for %q", boxSize, typ)
		}
		if off+boxSize > size {
			return merr.Newf(merr.StageParse, off, "box-header",
				"box %q size %d overruns input by %d bytes", typ, boxSize, off+boxSize-size)
		}
		off += boxSize
	}
	return nil
}

// verifyIndex checks that the time base is sane and every chunk
// reference lands inside the file and inside mdat.
func verifyIndex(info *MP4Info) error {
	if info.Timescale == 0 {
		return merr.Newf(merr.StageVerify, -1, "mdhd", "zero track timescale")
	}
	if info.SampleCount == 0 {
		return merr.Newf(merr.StageVerify, -1, "stsz", "no samples indexed")
	}
	var total uint64
	for _, s := range info.SampleSizes {
		total += uint64(s)
	}
	if uint32(len(info.SampleSizes)) != info.SampleCount {
		return merr.Newf(merr.StageVerify, -1, "stsz",
			"stsz entries %d != sample count %d", len(info.SampleSizes), info.SampleCount)
	}
	if total > info.MdatSize {
		return merr.Newf(merr.StageVerify, int64(info.MdatOffset), "stsz",
			"indexed sample bytes %d exceed mdat payload %d", total, info.MdatSize)
	}
	for _, co := range info.ChunkOffsets {
		if uint64(co) < info.MdatOffset || uint64(co) >= info.MdatOffset+info.MdatSize {
			return merr.Newf(merr.StageVerify, int64(co), "stco",
				"chunk offset outside mdat [%d,%d)", info.MdatOffset, info.MdatOffset+info.MdatSize)
		}
	}
	return nil
}
