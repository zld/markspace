package output

import (
	"bytes"

	"github.com/abema/go-mp4"

	"replayproof/internal/media"
	"replayproof/internal/parse"
)

// outTrack is everything needed to write one output trak.
type outTrack struct {
	kind      parse.Kind
	trackID   uint32
	timescale uint32
	samples   []outSample
	stsd      []byte
	width     uint16
	height    uint16
	channels  uint16
	rate      uint32
}

// outSample is one sample to write, with times already shifted to clip zero.
type outSample struct {
	dts      int64 // ticks
	dur      int64 // ticks
	pts      int64 // ticks, relative to dts
	offset   int64
	size     uint32
	keyframe bool
}

// buildMoov assembles the moov box for the sliced tracks. Times are shifted
// by the video start so the clip begins at zero.
func buildMoov(meta *parse.Meta, tracks []*sourceTrack) ([]byte, error) {
	if len(tracks) == 0 {
		return nil, media.New(media.CodeEventWindow, "clip.moov", -1, "no tracks")
	}
	shift := tracks[0].entries[0].DTS
	var outs []*outTrack
	for _, ts := range tracks {
		if ts.track.Timescale == 0 {
			return nil, media.New(media.CodeInvalidBox, "clip.moov", -1, "zero source timescale")
		}
		ot := &outTrack{
			kind:      ts.track.Kind,
			trackID:   ts.track.ID,
			timescale: ts.track.Timescale,
			stsd:      ts.track.StsdRaw,
			width:     ts.track.Width,
			height:    ts.track.Height,
			channels:  ts.track.ChannelCount,
			rate:      ts.track.SampleRate,
		}
		if ot.trackID == 0 {
			ot.trackID = uint32(len(outs) + 1)
		}
		for i, e := range ts.entries {
			start := e.DTS.Sub(shift)
			dur := e.End.Sub(e.DTS)
			if dur.Sign() <= 0 {
				dur = media.FromTicks(1, ot.timescale)
			}
			pts := e.PTS.Sub(shift).Sub(start)
			ot.samples = append(ot.samples, outSample{
				dts:      start.Scale(ot.timescale),
				dur:      dur.Scale(ot.timescale),
				pts:      pts.Scale(ot.timescale),
				offset:   ts.offsets[i],
				size:     e.Size,
				keyframe: e.Keyframe,
			})
		}
		outs = append(outs, ot)
	}

	var body []byte
	maxDur := int64(0)
	for _, ot := range outs {
		trak, dur, err := buildTrak(ot, meta)
		if err != nil {
			return nil, err
		}
		if dur > maxDur {
			maxDur = dur
		}
		body = append(body, trak...)
	}
	mvhdDur := uint64(maxDur)
	if len(outs) > 0 && outs[0].timescale != 0 {
		mvhdDur = uint64(media.FromTicks(maxDur, outs[0].timescale).Scale(1000))
	}
	mvhd, err := marshalBox(&mp4.Mvhd{
		FullBox:     mp4.FullBox{Version: 0},
		Timescale:   1000,
		DurationV0:  uint32(mvhdDur),
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
		NextTrackID: uint32(len(outs) + 1),
	})
	if err != nil {
		return nil, err
	}
	return wrapBox("moov", append(mvhd, body...)), nil
}

func buildTrak(ot *outTrack, meta *parse.Meta) ([]byte, int64, error) {
	matrix := [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	if ot.kind == parse.KindVideo && meta != nil && meta.Rotation != nil {
		switch meta.Rotation.Degrees {
		case 90:
			matrix = [9]int32{0, 0x00010000, 0, -0x00010000, 0, 0, 0, 0, 0x40000000}
		case 180:
			matrix = [9]int32{-0x00010000, 0, 0, 0, -0x00010000, 0, 0, 0, 0x40000000}
		case 270:
			matrix = [9]int32{0, -0x00010000, 0, 0x00010000, 0, 0, 0, 0, 0x40000000}
		}
	}
	var totalTicks int64
	for _, s := range ot.samples {
		totalTicks += s.dur
	}
	mediaDur := media.FromTicks(totalTicks, ot.timescale)
	tkhdDur := uint32(mediaDur.Scale(1000))
	volume := int16(0)
	if ot.kind == parse.KindAudio {
		volume = 0x0100
	}
	width := ot.width
	height := ot.height
	if ot.kind == parse.KindVideo && meta != nil && meta.Crop != nil {
		width = uint16(int(width) - int(meta.Crop.Left) - int(meta.Crop.Right))
		height = uint16(int(height) - int(meta.Crop.Top) - int(meta.Crop.Bottom))
	}
	tkhd, err := marshalBox(&mp4.Tkhd{
		FullBox:    mp4.FullBox{Version: 0, Flags: [3]byte{0, 0, 7}},
		TrackID:    ot.trackID,
		DurationV0: tkhdDur,
		Volume:     volume,
		Matrix:     matrix,
		Width:      uint32(width) << 16,
		Height:     uint32(height) << 16,
	})
	if err != nil {
		return nil, 0, err
	}
	mdhd, err := marshalBox(&mp4.Mdhd{
		FullBox:    mp4.FullBox{Version: 0},
		Timescale:  ot.timescale,
		DurationV0: uint32(totalTicks),
		Language:   [3]byte{'u', 'n', 'd'},
	})
	if err != nil {
		return nil, 0, err
	}
	handler := "vide"
	name := "VideoHandler"
	if ot.kind == parse.KindAudio {
		handler = "soun"
		name = "SoundHandler"
	}
	hdlr, err := marshalBox(&mp4.Hdlr{
		FullBox:     mp4.FullBox{Version: 0},
		HandlerType: [4]byte{handler[0], handler[1], handler[2], handler[3]},
		Name:        name,
	})
	if err != nil {
		return nil, 0, err
	}
	mediaHdr := buildMediaHeader(ot.kind)
	stbl, err := buildStbl(ot)
	if err != nil {
		return nil, 0, err
	}
	url := box("url ", fullBoxPrefix(0, [3]byte{0, 0, 1}))
	dref := box("dref", cat(fullBoxPrefix(0, [3]byte{0, 0, 0}), u32(1), url))
	dinf := box("dinf", dref)
	minf := box("minf", cat(mediaHdr, dinf, stbl))
	mdia := box("mdia", cat(mdhd, hdlr, minf))
	return box("trak", cat(tkhd, mdia)), totalTicks, nil
}

func buildMediaHeader(kind parse.Kind) []byte {
	if kind == parse.KindAudio {
		return box("smhd", cat(fullBoxPrefix(0, [3]byte{}), []byte{0, 0, 0, 0}))
	}
	return box("vmhd", cat(fullBoxPrefix(0, [3]byte{0, 0, 1}), make([]byte, 8)))
}

func buildStbl(ot *outTrack) ([]byte, error) {
	if len(ot.stsd) == 0 {
		return nil, media.New(media.CodeNoTrack, "clip.stsd", -1, "track has no sample description")
	}
	stsd := box("stsd", cat(fullBoxPrefix(0, [3]byte{}), u32(1), ot.stsd))
	// One sample per chunk keeps the chunk table trivially correct for
	// arbitrary slice lengths.
	offsets := make([]uint32, len(ot.samples))
	sizes := make([]uint32, len(ot.samples))
	keys := make([]uint32, 0, len(ot.samples))
	var stts []mp4.SttsEntry
	var ctts []mp4.CttsEntry
	haveCtts := false
	for i, s := range ot.samples {
		offsets[i] = uint32(s.offset)
		sizes[i] = s.size
		if s.keyframe {
			keys = append(keys, uint32(i+1))
		}
		if n := len(stts); n > 0 && stts[n-1].SampleDelta == uint32(s.dur) {
			stts[n-1].SampleCount++
		} else {
			stts = append(stts, mp4.SttsEntry{SampleCount: 1, SampleDelta: uint32(s.dur)})
		}
		if s.pts != 0 {
			haveCtts = true
		}
		if n := len(ctts); n > 0 && ctts[n-1].SampleOffsetV0 == uint32(int32(s.pts)) {
			ctts[n-1].SampleCount++
		} else {
			ctts = append(ctts, mp4.CttsEntry{SampleCount: 1, SampleOffsetV0: uint32(int32(s.pts))})
		}
	}
	sttsBytes, err := marshalBox(&mp4.Stts{
		FullBox:    mp4.FullBox{Version: 0},
		EntryCount: uint32(len(stts)),
		Entries:    stts,
	})
	if err != nil {
		return nil, err
	}
	stscBytes, err := marshalBox(&mp4.Stsc{
		FullBox:    mp4.FullBox{Version: 0},
		EntryCount: 1,
		Entries:    []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1}},
	})
	if err != nil {
		return nil, err
	}
	stszBytes, err := marshalBox(&mp4.Stsz{
		FullBox:     mp4.FullBox{Version: 0},
		SampleSize:  0,
		SampleCount: uint32(len(sizes)),
		EntrySize:   sizes,
	})
	if err != nil {
		return nil, err
	}
	stcoBytes, err := marshalBox(&mp4.Stco{
		FullBox:     mp4.FullBox{Version: 0},
		EntryCount:  uint32(len(offsets)),
		ChunkOffset: offsets,
	})
	if err != nil {
		return nil, err
	}
	out := cat(stsd, sttsBytes, stscBytes, stszBytes, stcoBytes)
	if ot.kind == parse.KindVideo {
		stssBytes, err := marshalBox(&mp4.Stss{
			FullBox:      mp4.FullBox{Version: 0},
			EntryCount:   uint32(len(keys)),
			SampleNumber: keys,
		})
		if err != nil {
			return nil, err
		}
		out = cat(out, stssBytes)
	}
	if haveCtts {
		cttsBytes, err := marshalBox(&mp4.Ctts{
			FullBox:    mp4.FullBox{Version: 0},
			EntryCount: uint32(len(ctts)),
			Entries:    ctts,
		})
		if err != nil {
			return nil, err
		}
		out = cat(out, cttsBytes)
	}
	return box("stbl", out), nil
}

func ftypBytes() ([]byte, error) {
	p, err := marshalBox(&mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', '5'},
		MinorVersion: 512,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', '5'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
			{CompatibleBrand: [4]byte{'i', 's', 'o', '6'}},
		},
	})
	if err != nil {
		return nil, media.Wrap(err, media.CodeInvalidBox, "clip.ftyp", -1)
	}
	return p, nil
}

// marshalBox marshals a box struct with its common header.
func marshalBox(b mp4.IImmutableBox) ([]byte, error) {
	var w sliceWriter
	if _, err := mp4.Marshal(&w, b, mp4.Context{}); err != nil {
		return nil, media.Wrap(err, media.CodeInvalidBox, "clip.marshal", -1)
	}
	return wrapBox(b.GetType().String(), w.buf), nil
}

type sliceWriter struct {
	buf []byte
}

func (w *sliceWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func wrapBox(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	putU32BE(out[:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func box(typ string, payload []byte) []byte { return wrapBox(typ, payload) }

func fullBoxPrefix(version byte, flags [3]byte) []byte {
	return []byte{version, flags[0], flags[1], flags[2]}
}

func u32(v uint32) []byte {
	var b [4]byte
	putU32BE(b[:], v)
	return b[:]
}

func putU32BE(dst []byte, v uint32) {
	dst[0] = byte(v >> 24)
	dst[1] = byte(v >> 16)
	dst[2] = byte(v >> 8)
	dst[3] = byte(v)
}

func cat(parts ...[]byte) []byte {
	var buf bytes.Buffer
	for _, p := range parts {
		buf.Write(p)
	}
	return buf.Bytes()
}
