// Package parse reads fragmented MP4 segments and MPEG-TS chunks,
// preserving raw byte offsets so truncation can be located exactly and
// indices can point back at their source bytes.
package parse

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/Eyevinn/mp4ff/mp4"

	"mediarun/internal/fault"
)

// Sample is one parsed media sample with its raw location.
type Sample struct {
	Index      int    // index within the fragment
	DecodeTime uint64 // raw decode time in track timescale units
	Duration   uint32 // duration in track timescale units
	Size       uint32
	Sync       bool  // random-access point
	DataOffset int64 // absolute byte offset of the payload in the file
}

// Fragment is one moof/mdat pair inside a media segment.
type Fragment struct {
	Path           string
	MoofOffset     int64 // absolute offset of the moof box
	SeqNumber      uint32
	TrackID        uint32
	BaseDecodeTime uint64 // raw tfdt value in track timescale units
	Samples        []Sample
}

// Segment is the parsed result of one media segment file.
type Segment struct {
	Path       string
	Fragments  []*Fragment
	MediaBytes int64 // total mdat payload bytes
}

// TrackInfo describes one track of an initialization segment.
type TrackInfo struct {
	ID        uint32
	Timescale uint32
}

// ParseInit reads an initialization segment and returns per-track info.
func ParseInit(path string, data []byte) (map[uint32]TrackInfo, error) {
	f, err := mp4.DecodeFile(bytes.NewReader(data))
	if err != nil {
		return nil, fault.New(fault.StageSampleTable, "cannot decode init segment").
			At(path, 0).Wrap(err)
	}
	if f.Moov == nil {
		return nil, fault.New(fault.StageSampleTable, "init segment has no moov").At(path, 0)
	}
	out := map[uint32]TrackInfo{}
	for _, trak := range f.Moov.Traks {
		if trak.Mdia == nil || trak.Mdia.Mdhd == nil {
			return nil, fault.New(fault.StageSampleTable, "track missing mdhd").
				At(path, 0).For(fmt.Sprint(trak.Tkhd.TrackID), -1)
		}
		ts := trak.Mdia.Mdhd.Timescale
		if ts == 0 {
			return nil, fault.New(fault.StageSampleTable, "zero media timescale").
				At(path, 0).For(fmt.Sprint(trak.Tkhd.TrackID), -1)
		}
		out[trak.Tkhd.TrackID] = TrackInfo{ID: trak.Tkhd.TrackID, Timescale: ts}
	}
	if len(out) == 0 {
		return nil, fault.New(fault.StageSampleTable, "init segment has no tracks").At(path, 0)
	}
	return out, nil
}

// boxSpan records one top-level box.
type boxSpan struct {
	typ         string
	start, size int64 // size includes header
	hdrLen      int64
}

// walkBoxes validates the top-level box structure and classifies
// truncation: an incomplete box header, a truncated sample table (moof)
// or a truncated media payload (mdat) each report their own stage and
// the first unreadable offset.
func walkBoxes(path string, data []byte) ([]boxSpan, error) {
	var spans []boxSpan
	pos := int64(0)
	total := int64(len(data))
	for pos < total {
		if total-pos < 8 {
			return nil, fault.New(fault.StageBoxHeader, "truncated box header").
				At(path, pos)
		}
		size32 := binary.BigEndian.Uint32(data[pos:])
		typ := string(data[pos+4 : pos+8])
		hdrLen := int64(8)
		var size int64
		switch size32 {
		case 1:
			if total-pos < 16 {
				return nil, fault.New(fault.StageBoxHeader, "truncated largesize header").
					At(path, pos)
			}
			size = int64(binary.BigEndian.Uint64(data[pos+8:]))
			hdrLen = 16
		case 0: // box extends to end of file
			size = total - pos
		default:
			size = int64(size32)
		}
		if size < hdrLen {
			return nil, fault.New(fault.StageBoxHeader, fmt.Sprintf("invalid box size %d", size)).
				At(path, pos)
		}
		if pos+size > total {
			stage := fault.StageBoxHeader
			switch typ {
			case "moof":
				stage = fault.StageSampleTable
			case "mdat":
				stage = fault.StageMediaPayload
			}
			return nil, fault.New(stage, fmt.Sprintf("truncated %s box", typ)).
				At(path, total) // first unreadable offset
		}
		spans = append(spans, boxSpan{typ: typ, start: pos, size: size, hdrLen: hdrLen})
		pos += size
	}
	return spans, nil
}

// ParseMediaSegment parses one fragmented MP4 media segment. On any
// truncation it returns a classified fault and no usable index data.
func ParseMediaSegment(path string, data []byte) (*Segment, error) {
	spans, err := walkBoxes(path, data)
	if err != nil {
		return nil, err
	}
	seg := &Segment{Path: path}
	for _, sp := range spans {
		switch sp.typ {
		case "mdat":
			seg.MediaBytes += sp.size - sp.hdrLen
		case "moof":
			frag, err := parseMoof(path, data[sp.start:sp.start+sp.size], sp.start)
			if err != nil {
				return nil, err
			}
			seg.Fragments = append(seg.Fragments, frag)
		}
	}
	if len(seg.Fragments) == 0 {
		return nil, fault.New(fault.StageSampleTable, "segment contains no moof").At(path, 0)
	}
	return seg, nil
}

// parseMoof decodes one moof box and resolves sample boundaries with
// absolute file offsets.
func parseMoof(path string, moofData []byte, moofStart int64) (*Fragment, error) {
	box, err := mp4.DecodeBox(uint64(moofStart), bytes.NewReader(moofData))
	if err != nil {
		if err == io.ErrUnexpectedEOF {
			return nil, fault.New(fault.StageSampleTable, "truncated sample table").
				At(path, moofStart+int64(len(moofData)))
		}
		return nil, fault.New(fault.StageSampleTable, "cannot decode moof").
			At(path, moofStart).Wrap(err)
	}
	moof, ok := box.(*mp4.MoofBox)
	if !ok {
		return nil, fault.New(fault.StageSampleTable, "box is not a moof").At(path, moofStart)
	}
	if moof.Mfhd == nil || len(moof.Trafs) == 0 {
		return nil, fault.New(fault.StageSampleTable, "moof missing mfhd/traf").At(path, moofStart)
	}
	traf := moof.Trafs[0]
	if traf.Tfhd == nil || traf.Tfdt == nil {
		return nil, fault.New(fault.StageSampleTable, "traf missing tfhd/tfdt").At(path, moofStart)
	}
	frag := &Fragment{
		Path:           path,
		MoofOffset:     moofStart,
		SeqNumber:      moof.Mfhd.SequenceNumber,
		TrackID:        traf.Tfhd.TrackID,
		BaseDecodeTime: traf.Tfdt.BaseMediaDecodeTime(),
	}
	decodeTime := frag.BaseDecodeTime
	for _, trun := range traf.Truns {
		if !trun.HasDataOffset() {
			return nil, fault.New(fault.StageSampleTable, "trun without data offset unsupported").
				At(path, moofStart)
		}
		dataOff := moofStart + int64(trun.DataOffset)
		for i, s := range trun.Samples {
			dur := s.Dur
			if !trun.HasSampleDuration() {
				dur = traf.Tfhd.DefaultSampleDuration
			}
			size := s.Size
			if !trun.HasSampleSize() {
				size = traf.Tfhd.DefaultSampleSize
			}
			flags := s.Flags
			if !trun.HasSampleFlags() {
				flags = traf.Tfhd.DefaultSampleFlags
			}
			if i == 0 {
				if ff, ok := trun.FirstSampleFlags(); ok {
					flags = ff
				}
			}
			frag.Samples = append(frag.Samples, Sample{
				Index:      len(frag.Samples),
				DecodeTime: decodeTime,
				Duration:   dur,
				Size:       size,
				Sync:       mp4.IsSyncSampleFlags(flags),
				DataOffset: dataOff,
			})
			dataOff += int64(size)
			decodeTime += uint64(dur)
		}
	}
	if len(frag.Samples) == 0 {
		return nil, fault.New(fault.StageSampleTable, "fragment has no samples").At(path, moofStart)
	}
	return frag, nil
}
