package fixture

import (
	"bytes"
	"fmt"

	"github.com/abema/go-mp4"
)

// MP4Sample builds a minimal, structurally valid MP4 (ftyp + mdat + moov with
// mvhd/trak) that the go-mp4 library can parse. It exists so the demo can
// cross-check MP4 structure handling with go-mp4 v1.3.0 without shipping an
// external media file.
type MP4Sample struct {
	Data      []byte
	TrackID   uint32
	Timescale uint32
	Duration  uint64
}

// DefaultMP4 returns a small MP4 with one track.
func DefaultMP4() MP4Sample {
	return MP4Sample{
		Data:      buildMP4(),
		TrackID:   1,
		Timescale: 1000,
		Duration:  2000,
	}
}

// buildMP4 writes ftyp, mdat and a moov containing mvhd and one trak with
// tkhd/mdia(mdhd/hdlr/minf(stbl(stsd/stts/stsc/stsz/stco))). Only the boxes
// needed for go-mp4's structural probe are populated.
func buildMP4() []byte {
	box := func(typ string, payload []byte) []byte {
		size := 8 + len(payload)
		out := make([]byte, 8, size)
		putU32(out[0:], uint32(size))
		copy(out[4:8], typ)
		return append(out, payload...)
	}
	full := func(typ string, version byte, body []byte) []byte {
		payload := append([]byte{version, 0, 0, 0}, body...)
		return box(typ, payload)
	}

	// ftyp: major brand isom, minor 0, compatible isom/iso2/mp41.
	ftyp := box("ftyp", concat(
		[]byte("isom"), putU32(nil, 0),
		[]byte("isom"), []byte("iso2"), []byte("mp41")))

	mdat := box("mdat", mediaFrame(0, 64))

	mvhdBody := make([]byte, 96)
	putU32(mvhdBody[8:], 1000)  // timescale
	putU32(mvhdBody[12:], 2000) // duration
	mvhd := full("mvhd", 0, mvhdBody)

	tkhdBody := make([]byte, 80)
	putU32(tkhdBody[8:], 1) // track id
	tkhd := full("tkhd", 0, tkhdBody)

	mdhdBody := make([]byte, 20)
	putU32(mdhdBody[8:], 1000)
	putU32(mdhdBody[12:], 1000)
	mdhd := full("mdhd", 0, mdhdBody)

	hdlrBody := make([]byte, 24)
	copy(hdlrBody[4:], []byte("vide"))
	hdlr := full("hdlr", 0, hdlrBody)

	// stsd with no entries, plus empty stts/stsc/stsz/stco for a valid stbl.
	stsd := full("stsd", 0, putU32(nil, 0))
	stts := full("stts", 0, putU32(nil, 0))
	stsc := full("stsc", 0, putU32(nil, 0))
	stsz := full("stsz", 0, concat(putU32(nil, 0), putU32(nil, 0)))
	stco := full("stco", 0, putU32(nil, 0))
	stbl := box("stbl", concat(stsd, stts, stsc, stsz, stco))

	minf := box("minf", stbl)
	mdia := box("mdia", concat(mdhd, hdlr, minf))
	trak := box("trak", concat(tkhd, mdia))
	moov := box("moov", concat(mvhd, trak))

	return concat(ftyp, mdat, moov)
}

// ProbeMP4 parses the sample with go-mp4 and returns the structural summary
// used in the demo: major brand, track count and the first track's timescale.
// A parse failure is reported so the demo can demonstrate structure checking.
func ProbeMP4(data []byte) (brand string, tracks int, timescale uint32, err error) {
	info, err := mp4.Probe(bytes.NewReader(data))
	if err != nil {
		return "", 0, 0, err
	}
	brand = string(info.MajorBrand[:])
	tracks = len(info.Tracks)
	if len(info.Tracks) > 0 {
		timescale = info.Tracks[0].Timescale
	}
	return brand, tracks, timescale, nil
}

// ValidateStructure walks every box with go-mp4's ReadBoxStructure and reports
// the first structural defect (a truncated or over-long box) with its byte
// offset. This catches damage that Probe alone tolerates.
func ValidateStructure(data []byte) error {
	var firstErr error
	_, err := mp4.ReadBoxStructure(bytes.NewReader(data), func(h *mp4.ReadHandle) (interface{}, error) {
		if firstErr != nil {
			return nil, nil
		}
		if _, e := h.Expand(); e != nil {
			firstErr = fmt.Errorf("box %s @%d: %w", h.BoxInfo.Type.String(), h.BoxInfo.Offset, e)
		}
		return nil, nil
	})
	if firstErr != nil {
		return firstErr
	}
	return err
}

func putU32(dst []byte, v uint32) []byte {
	if dst == nil {
		dst = make([]byte, 4)
	}
	dst[0] = byte(v >> 24)
	dst[1] = byte(v >> 16)
	dst[2] = byte(v >> 8)
	dst[3] = byte(v)
	return dst
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
