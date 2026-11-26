package ingest

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"pwf/internal/media"
)

// MP4Sample is one audio sample with its file position and integer
// duration expressed in the track's mdhd timescale.
type MP4Sample struct {
	Index    int
	Offset   int64
	Size     uint32
	Duration media.Duration
}

// MP4TrackResult is the parsed representation of one MP4 audio track.
type MP4TrackResult struct {
	Track      string
	Codec      string
	Timescale  uint32
	Samples    []MP4Sample
	TotalTicks int64
}

// boxHeader describes one ISO-BMFF box at a given file offset.
type boxHeader struct {
	offset  int64
	size    int64
	hdrLen  int
	boxType string
}

// scanBoxes performs a streaming structural pass over an MP4 file,
// validating every box header and the declared extent against the file
// size. Only fixed-size headers are read via ReadAt, so memory use stays
// bounded regardless of a box's declared size. A truncated box produces a
// media.Error pinned to the byte offset where the box starts. Only the
// 8-byte header of each box is read here; payload parsing stays in
// go-mp4's reader.
func scanBoxes(file *os.File) error {
	fi, err := file.Stat()
	if err != nil {
		return media.NewError(media.KindCorrupt, media.StageParseMP4, "cannot stat mp4", err)
	}
	fileSize := fi.Size()
	var walk func(start, end int64, depth int) error
	walk = func(start, end int64, depth int) error {
		pos := start
		for pos+8 <= end {
			hdr, err := readBoxHeader(file, pos, end, fileSize)
			if err != nil {
				return err
			}
			if isContainerType(hdr.boxType) {
				payloadStart := pos + int64(hdr.hdrLen)
				payloadEnd := pos + hdr.size
				if err := walk(payloadStart, payloadEnd, depth+1); err != nil {
					return err
				}
			}
			pos += hdr.size
		}
		if pos != end {
			return media.NewError(media.KindTruncated, media.StageParseMP4,
				fmt.Sprintf("stray %d bytes inside box region ending at %d", end-pos, end), nil).
				AtOffset(pos)
		}
		return nil
	}
	return walk(0, fileSize, 0)
}

func readBoxHeader(file *os.File, pos, regionEnd, fileSize int64) (boxHeader, error) {
	var hdr [8]byte
	n, err := file.ReadAt(hdr[:], pos)
	if err != nil && err != io.EOF {
		return boxHeader{}, media.NewError(media.KindCorrupt, media.StageParseMP4,
			"cannot read box header", err).AtOffset(pos)
	}
	if n < 8 {
		return boxHeader{}, media.NewError(media.KindTruncated, media.StageParseMP4,
			fmt.Sprintf("box header truncated: %d of 8 bytes at offset %d", n, pos), nil).
			AtOffset(pos)
	}
	size := int64(binary.BigEndian.Uint32(hdr[0:4]))
	boxType := string(hdr[4:8])
	hdrLen := 8
	switch size {
	case 0:
		size = fileSize - pos // box extends to EOF
	case 1:
		var ext [8]byte
		if en, e := file.ReadAt(ext[:], pos+8); e != nil || en < 8 {
			return boxHeader{}, media.NewError(media.KindTruncated, media.StageParseMP4,
				"64-bit largesize box header truncated", e).AtOffset(pos)
		}
		size = int64(binary.BigEndian.Uint64(ext[:]))
		hdrLen = 16
	}
	if size < int64(hdrLen) {
		return boxHeader{}, media.NewError(media.KindBadLength, media.StageParseMP4,
			fmt.Sprintf("box %q declares impossible size %d at offset %d", boxType, size, pos), nil).
			AtOffset(pos)
	}
	if pos+size > regionEnd || pos+size > fileSize {
		return boxHeader{}, media.NewError(media.KindTruncated, media.StageParseMP4,
			fmt.Sprintf("box %q size %d at offset %d exceeds file/container end %d",
				boxType, size, pos, min64(regionEnd, fileSize)), nil).AtOffset(pos)
	}
	return boxHeader{offset: pos, size: size, hdrLen: hdrLen, boxType: boxType}, nil
}

// container boxes whose payload consists solely of child boxes.
var containerTypes = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true,
	"stbl": true, "dinf": true, "udta": true, "edts": true,
}

func isContainerType(t string) bool { return containerTypes[t] }

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// ParseMP4 validates structure then extracts the first audio track's
// timescale and per-sample timing using go-mp4. Sample durations are read
// from the stts run-length table and stay integral in the mdhd clock.
func ParseMP4(path, track, codec string) (*MP4TrackResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, media.NewError(media.KindCorrupt, media.StageParseMP4,
			"cannot open mp4 source", err).WithTrack(track)
	}
	defer file.Close()

	if err := scanBoxes(file); err != nil {
		if me, ok := media.AsError(err); ok {
			return nil, me.WithTrack(track)
		}
		return nil, err
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	bi, err := mp4.ExtractBoxes(file, nil, []mp4.BoxPath{
		{mp4.StrToBoxType("moov"), mp4.StrToBoxType("trak"), mp4.StrToBoxType("mdia"), mp4.StrToBoxType("mdhd")},
		{mp4.StrToBoxType("moov"), mp4.StrToBoxType("trak"), mp4.StrToBoxType("mdia"), mp4.StrToBoxType("minf"),
			mp4.StrToBoxType("stbl"), mp4.StrToBoxType("stts")},
		{mp4.StrToBoxType("moov"), mp4.StrToBoxType("trak"), mp4.StrToBoxType("mdia"), mp4.StrToBoxType("minf"),
			mp4.StrToBoxType("stbl"), mp4.StrToBoxType("stsz")},
		{mp4.StrToBoxType("moov"), mp4.StrToBoxType("trak"), mp4.StrToBoxType("mdia"), mp4.StrToBoxType("minf"),
			mp4.StrToBoxType("stbl"), mp4.StrToBoxType("stco")},
	})
	if err != nil {
		return nil, media.NewError(media.KindCorrupt, media.StageParseMP4,
			"required track boxes missing", err).WithTrack(track)
	}
	if len(bi) < 4 {
		return nil, media.NewError(media.KindCorrupt, media.StageParseMP4,
			fmt.Sprintf("expected mdhd/stts/stsz/stco, found %d boxes", len(bi)), nil).
			WithTrack(track)
	}

	mdhd, ok1 := decodeBox[*mp4.Mdhd](file, bi[0])
	stts, ok2 := decodeBox[*mp4.Stts](file, bi[1])
	stsz, ok3 := decodeBox[*mp4.Stsz](file, bi[2])
	stco, ok4 := decodeBox[*mp4.Stco](file, bi[3])
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return nil, media.NewError(media.KindCorrupt, media.StageParseMP4,
			"failed to decode timing boxes", nil).WithTrack(track)
	}
	if mdhd.Timescale == 0 {
		return nil, media.NewError(media.KindUndetermined, media.StageParseMP4,
			"mdhd declares zero timescale", nil).WithTrack(track)
	}

	sizes := sampleSizes(stsz)
	deltas, err := expandDeltas(stts, len(sizes))
	if err != nil {
		return nil, media.NewError(media.KindCorrupt, media.StageParseMP4, err.Error(), err).WithTrack(track)
	}
	if len(stco.ChunkOffset) == 0 {
		return nil, media.NewError(media.KindCorrupt, media.StageParseMP4,
			"stco has no chunk offsets", nil).WithTrack(track)
	}

	tb, err := media.NewTimeBase(mdhd.Timescale)
	if err != nil {
		return nil, media.NewError(media.KindUndetermined, media.StageParseMP4,
			"invalid mdhd timescale", err).WithTrack(track)
	}
	// One chunk containing all samples: sample offsets follow chunk start.
	samples := make([]MP4Sample, len(sizes))
	cursor := int64(stco.ChunkOffset[0])
	var total int64
	for i, sz := range sizes {
		d, err := media.NewDuration(int64(deltas[i]), tb)
		if err != nil {
			return nil, media.NewError(media.KindUndetermined, media.StageParseMP4,
				fmt.Sprintf("sample %d has invalid duration", i), err).WithTrack(track)
		}
		samples[i] = MP4Sample{Index: i, Offset: cursor, Size: sz, Duration: d}
		cursor += int64(sz)
		total += int64(deltas[i])
	}
	return &MP4TrackResult{
		Track: track, Codec: codec, Timescale: mdhd.Timescale,
		Samples: samples, TotalTicks: total,
	}, nil
}

// decodeBox decodes a previously located leaf box and asserts its type T.
func decodeBox[T mp4.IBox](file *os.File, info *mp4.BoxInfo) (T, bool) {
	if _, err := info.SeekToPayload(file); err != nil {
		var z T
		return z, false
	}
	box, _, err := mp4.UnmarshalAny(file, info.Type, info.Size-info.HeaderSize, mp4.Context{})
	if err != nil {
		var z T
		return z, false
	}
	typed, ok := box.(T)
	return typed, ok
}

// sampleSizes resolves stsz, honoring both the uniform-size form and the
// per-sample entry table.
func sampleSizes(stsz *mp4.Stsz) []uint32 {
	if stsz.SampleSize != 0 {
		out := make([]uint32, stsz.SampleCount)
		for i := range out {
			out[i] = stsz.SampleSize
		}
		return out
	}
	return append([]uint32(nil), stsz.EntrySize...)
}

// expandDeltas turns the stts run-length table into one delta per sample.
func expandDeltas(stts *mp4.Stts, sampleCount int) ([]uint32, error) {
	out := make([]uint32, 0, sampleCount)
	for _, e := range stts.Entries {
		if e.SampleDelta == 0 {
			return nil, fmt.Errorf("stts entry with zero delta")
		}
		for i := uint32(0); i < e.SampleCount; i++ {
			out = append(out, e.SampleDelta)
		}
	}
	if len(out) != sampleCount {
		return nil, fmt.Errorf("stts covers %d samples but stsz lists %d", len(out), sampleCount)
	}
	return out, nil
}
