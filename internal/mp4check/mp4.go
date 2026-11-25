// Package mp4check parses MP4/ISOBMFF container and track structure with
// go-mp4. It performs structure verification only; it does not decode codecs.
package mp4check

import (
	"bytes"
	"encoding/binary"

	"github.com/abema/go-mp4"

	"assetcheck/internal/errmodel"
)

// TrackInfo is the extracted per-track structure facts.
type TrackInfo struct {
	Index     int    `json:"index"`
	Timescale uint32 `json:"timescale"`
	Duration  uint64 `json:"duration"`
}

// ContainerInfo summarizes box layout and tracks.
type ContainerInfo struct {
	BoxTypes   []string    `json:"box-types"`
	HasFTYP    bool        `json:"has-ftyp"`
	HasMOOV    bool        `json:"has-moov"`
	Tracks     []TrackInfo `json:"tracks"`
	TrackCount int         `json:"track-count"`
}

// Analyze walks the box hierarchy and extracts track (trak/mdia/mdhd)
// timescales. Malformed containers return an errmodel format error.
func Analyze(data []byte) (*ContainerInfo, error) {
	info := &ContainerInfo{}
	r := bytes.NewReader(data)
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (any, error) {
		typeName := h.BoxInfo.Type.String()
		info.BoxTypes = append(info.BoxTypes, pathString(h.Path))
		switch typeName {
		case "ftyp":
			info.HasFTYP = true
		case "moov":
			info.HasMOOV = true
		case "trak":
			info.Tracks = append(info.Tracks, TrackInfo{Index: len(info.Tracks)})
		case "mdhd":
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, errmodel.New(errmodel.ErrMetadataEncoding, "parse mdhd: %v", perr)
			}
			mdhd, ok := box.(*mp4.Mdhd)
			if !ok {
				return nil, errmodel.New(errmodel.ErrMetadataEncoding, "mdhd payload has unexpected type %T", box)
			}
			if len(info.Tracks) == 0 {
				info.Tracks = append(info.Tracks, TrackInfo{})
			}
			idx := len(info.Tracks) - 1
			info.Tracks[idx].Timescale = mdhd.Timescale
			if mdhd.GetVersion() == 1 {
				info.Tracks[idx].Duration = mdhd.DurationV1
			} else {
				info.Tracks[idx].Duration = uint64(mdhd.DurationV0)
			}
		}
		// Recurse into container boxes; no-op for leaf boxes.
		return h.Expand()
	})
	if err != nil {
		return nil, errmodel.New(errmodel.ErrChunkOutOfBounds, "mp4 box structure: %v", err)
	}
	info.TrackCount = len(info.Tracks)
	if !info.HasFTYP {
		return info, errmodel.New(errmodel.ErrBadRIFF, "mp4 missing ftyp box")
	}
	if !info.HasMOOV {
		return info, errmodel.New(errmodel.ErrMissingDataChunk, "mp4 missing moov box")
	}
	return info, nil
}

func pathString(p mp4.BoxPath) string {
	b := make([]byte, 0, len(p)*5)
	for i, t := range p {
		if i > 0 {
			b = append(b, '/')
		}
		b = append(b, t[:]...)
	}
	return string(b)
}

// ---- minimal deterministic MP4 builder for self-generated fixtures ----

func box(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func fullBox(version byte, flags [3]byte, payload []byte) []byte {
	fb := []byte{version, flags[0], flags[1], flags[2]}
	return append(fb, payload...)
}

func mdhdBox(timescale uint32, duration uint32) []byte {
	p := make([]byte, 20) // after the 4-byte FullBox
	binary.BigEndian.PutUint32(p[8:12], timescale)
	binary.BigEndian.PutUint32(p[12:16], duration)
	// language undetermined ('und' = 0x55C4) and pre-defined zero.
	binary.BigEndian.PutUint16(p[16:18], 0x55C4)
	return box("mdhd", fullBox(0, [3]byte{}, p))
}

func hdlrBox() []byte {
	p := make([]byte, 24)
	binary.BigEndian.PutUint32(p[4:8], 0) // handler type placeholder
	copy(p[4:8], "soun")
	copy(p[8:20], "SoundHandler")
	return box("hdlr", fullBox(0, [3]byte{}, p))
}

func mediaBox(timescale uint32, duration uint32) []byte {
	var m []byte
	m = append(m, mdhdBox(timescale, duration)...)
	m = append(m, hdlrBox()...)
	return box("mdia", m)
}

func trackBox(trackID uint32, timescale uint32, duration uint32) []byte {
	tkhdPayload := make([]byte, 80) // v0 tkhd body after FullBox is 80 bytes
	binary.BigEndian.PutUint32(tkhdPayload[0:4], 0)
	binary.BigEndian.PutUint32(tkhdPayload[4:8], 0)
	binary.BigEndian.PutUint32(tkhdPayload[8:12], trackID)
	var t []byte
	t = append(t, box("tkhd", fullBox(0, [3]byte{0, 0, 3}, tkhdPayload))...)
	t = append(t, mediaBox(timescale, duration)...)
	return box("trak", t)
}

// MinimalMP4 builds a tiny ftyp+moov container with one track per supplied
// timescale (duration 96000 timescale units when nonzero).
func MinimalMP4(timescales ...uint32) []byte {
	if len(timescales) == 0 {
		timescales = []uint32{48000}
	}
	ftypPayload := append([]byte("isom"), 0x00, 0x00, 0x02, 0x00)
	ftypPayload = append(ftypPayload, []byte("isomiso2")...)
	ftyp := box("ftyp", ftypPayload)

	mvhdPayload := make([]byte, 96) // v0 body
	ts := timescales[0]
	binary.BigEndian.PutUint32(mvhdPayload[8:12], ts)
	binary.BigEndian.PutUint32(mvhdPayload[12:16], 96000)
	binary.BigEndian.PutUint32(mvhdPayload[16:20], 0x00010000) // rate 1.0
	mvhd := box("mvhd", fullBox(0, [3]byte{}, mvhdPayload))

	var traks []byte
	for i, t := range timescales {
		traks = append(traks, trackBox(uint32(i+1), t, 96000)...)
	}
	moov := box("moov", append(mvhd, traks...))
	return append(ftyp, moov...)
}
