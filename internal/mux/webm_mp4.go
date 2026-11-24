package mux

import (
	"bytes"

	mp4 "github.com/abema/go-mp4"

	"mediapack/internal/track"
)

// Matroska element IDs used when rebuilding a WebM container.
const (
	idEBMLHeader  uint32 = 0x1A45DFA3
	idSegment     uint32 = 0x18538067
	idInfo        uint32 = 0x1549A966
	idTracks      uint32 = 0x1654AE6B
	idTrackEntry  uint32 = 0xAE
	idTrackNumber uint32 = 0xD7
	idTrackUID    uint32 = 0x73C5
	idTrackType   uint32 = 0x83
	idCodecID     uint32 = 0x86
	idCluster     uint32 = 0x1F43B675
	idTimestamp   uint32 = 0xE7
	idSimpleBlock uint32 = 0xA3
	idTimeScale   uint32 = 0x2AD7B1
)

func idBytes(id uint32) []byte {
	switch {
	case id <= 0xFF:
		return []byte{byte(id)}
	case id <= 0xFFFF:
		return []byte{byte(id >> 8), byte(id)}
	case id <= 0xFFFFFF:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	default:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	}
}

func sizeVINT(v uint64) []byte {
	for width := 1; width <= 8; width++ {
		if v < (uint64(1)<<(7*width))-1 {
			out := make([]byte, width)
			val := v | (uint64(1) << (7 * width))
			for i := width - 1; i >= 0; i-- {
				out[i] = byte(val >> (8 * (width - 1 - i)))
			}
			return out
		}
	}
	panic("EBML size overflow")
}

func writeElement(buf *bytes.Buffer, id uint32, payload []byte) {
	buf.Write(idBytes(id))
	buf.Write(sizeVINT(uint64(len(payload))))
	buf.Write(payload)
}

func writeUintChild(buf *bytes.Buffer, id uint32, v uint64) {
	var raw []byte
	if v == 0 {
		raw = []byte{0}
	} else {
		for x := v; x > 0; x >>= 8 {
			raw = append([]byte{byte(x)}, raw...)
		}
	}
	writeElement(buf, id, raw)
}

func writeStrChild(buf *bytes.Buffer, id uint32, s string) {
	writeElement(buf, id, []byte(s))
}

func writeEBMLHead(buf *bytes.Buffer) {
	var body bytes.Buffer
	writeUintChild(&body, 0x4286, 1) // EBMLVersion
	writeUintChild(&body, 0x42F7, 1) // EBMLReadVersion
	writeUintChild(&body, 0x42F2, 4) // EBMLMaxIDLength
	writeUintChild(&body, 0x42F3, 8) // EBMLMaxSizeLength
	writeStrChild(&body, 0x4282, "webm")
	writeUintChild(&body, 0x4287, 2)
	writeUintChild(&body, 0x4285, 2)
	writeElement(buf, idEBMLHeader, body.Bytes())
}

func writeInfoAndTracks(seg *bytes.Buffer, mapping *track.Mapping) {
	var info bytes.Buffer
	writeUintChild(&info, idTimeScale, 1_000_000) // 1ms
	writeElement(seg, idInfo, info.Bytes())

	var tracks bytes.Buffer
	for _, t := range mapping.Tracks {
		var entry bytes.Buffer
		writeUintChild(&entry, idTrackNumber, uint64(t.InputNumber))
		writeUintChild(&entry, idTrackUID, uint64(1000+t.OutputIndex))
		ttype := uint64(1)
		if t.Codec.Kind == track.KindAudio {
			ttype = 2
		} else if t.Codec.Kind == track.KindSubtitle {
			ttype = 17
		}
		writeUintChild(&entry, idTrackType, ttype)
		writeStrChild(&entry, idCodecID, t.Codec.CodecID)
		writeElement(&tracks, idTrackEntry, entry.Bytes())
	}
	writeElement(seg, idTracks, tracks.Bytes())
}

func encodeWebMBlock(trackNum uint32, rel int16, keyframe bool, payload []byte) []byte {
	flags := byte(0)
	if keyframe {
		flags |= 0x80
	}
	head := []byte{0x80 | byte(trackNum), byte(rel >> 8), byte(rel), flags}
	return append(head, payload...)
}

// --- MP4 moov construction ----------------------------------------------

// mp4SampleLike is the per-sample record shared with mux.go's WriteMP4.
type mp4SampleLike struct {
	track uint32
	size  uint32
	off   int64
	hash  [32]byte
	seq   int
}

func samplesFor(all []mp4SampleLike, outTrack uint32) []mp4SampleLike {
	var out []mp4SampleLike
	for _, s := range all {
		if s.track == outTrack {
			out = append(out, s)
		}
	}
	return out
}

// buildMoov writes a minimal moov with one trak per output track. Sample
// tables use single-entry stts/stsc/stco and per-sample stsz.
func buildMoov(mapping *track.Mapping, samples []mp4SampleLike) ([]byte, error) {
	var mvhd bytes.Buffer
	mvhd.Write(be32(0))                  // version+flags
	mvhd.Write(be32(0))                  // creation
	mvhd.Write(be32(0))                  // modification
	mvhd.Write(be32(1000))               // timescale
	mvhd.Write(be32(0))                  // duration (placeholder)
	mvhd.Write([]byte{0x00, 0x01, 0, 0}) // rate 1.0
	mvhd.Write([]byte{0x01, 0x00})       // volume
	mvhd.Write(make([]byte, 10))         // reserved
	mvhd.Write(identityMatrix())
	mvhd.Write(make([]byte, 24)) // pre-defined
	mvhd.Write(be32(2))          // next track ID

	var out bytes.Buffer
	boxBytes(&out, mp4.BoxTypeMvhd(), mvhd.Bytes())

	for _, t := range mapping.Tracks {
		trak, err := buildTrak(t, samplesFor(samples, t.OutputIndex))
		if err != nil {
			return nil, err
		}
		out.Write(trak)
	}
	var moov bytes.Buffer
	boxBytes(&moov, mp4.BoxTypeMoov(), out.Bytes())
	return moov.Bytes(), nil
}

// boxBytes wraps an in-memory payload with an 8-byte box header.
func boxBytes(buf *bytes.Buffer, typ mp4.BoxType, payload []byte) {
	buf.Write(be32(uint32(8 + len(payload))))
	buf.Write(typ[:])
	buf.Write(payload)
}

func identityMatrix() []byte {
	return []byte{
		0x00, 0x01, 0x00, 0x00, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0x00, 0x01, 0x00, 0x00, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0x40, 0x00, 0x00, 0x00,
	}
}

func buildTrak(t *track.Track, samples []mp4SampleLike) ([]byte, error) {
	// tkhd
	var tkhd bytes.Buffer
	tkhd.Write([]byte{0x00, 0x00, 0x00, 0x03}) // version 0, flags enabled+in movie
	tkhd.Write(be32(0))
	tkhd.Write(be32(0))
	tkhd.Write(be32(t.OutputIndex)) // track ID
	tkhd.Write(be32(0))             // reserved
	tkhd.Write(be32(0))             // duration
	tkhd.Write(make([]byte, 8))     // reserved
	tkhd.Write([]byte{0, 0})        // layer
	tkhd.Write([]byte{0, 0})        // alt group
	tkhd.Write([]byte{0, 0})        // volume
	tkhd.Write(make([]byte, 2))     // reserved
	tkhd.Write(identityMatrix())
	tkhd.Write(append(be32(320<<16), be32(240<<16)...)) // width/height 16.16

	// mdia > mdhd
	var mdhd bytes.Buffer
	mdhd.Write(be32(0))
	mdhd.Write(be32(0))
	mdhd.Write(be32(0))
	mdhd.Write(be32(1000))         // timescale
	mdhd.Write(be32(0))            // duration
	mdhd.Write([]byte{0x55, 0xC4}) // language 'und'
	mdhd.Write([]byte{0, 0})       // pre-defined

	// hdlr
	var hdlr bytes.Buffer
	hdlr.Write(be32(0))
	hdlr.Write(be32(0))
	handler := []byte("vide")
	if t.Codec.Kind == track.KindAudio {
		handler = []byte("soun")
	} else if t.Codec.Kind == track.KindSubtitle {
		handler = []byte("text")
	}
	hdlr.Write(handler)
	hdlr.Write(make([]byte, 12))
	hdlr.Write([]byte(t.Codec.Name + "\x00"))

	// minf > stbl sample tables.
	stbl := buildStbl(t, samples)

	var minf bytes.Buffer
	boxBytes(&minf, mp4.BoxTypeStbl(), stbl)
	var mdia bytes.Buffer
	boxBytes(&mdia, mp4.BoxTypeMdhd(), mdhd.Bytes())
	boxBytes(&mdia, mp4.BoxTypeHdlr(), hdlr.Bytes())
	boxBytes(&mdia, mp4.BoxTypeMinf(), minf.Bytes())

	var trak bytes.Buffer
	boxBytes(&trak, mp4.BoxTypeTkhd(), tkhd.Bytes())
	boxBytes(&trak, mp4.BoxTypeMdia(), mdia.Bytes())
	return trak.Bytes(), nil
}

// buildStbl assembles stsd/stts/stsc/stsz/stco. Offsets are written relative
// to the mdat payload start (the first sample begins at mdat data offset 0).
func buildStbl(t *track.Track, samples []mp4SampleLike) []byte {
	var stbl bytes.Buffer

	// stsd: one generic sample entry identified by codec.
	var stsd bytes.Buffer
	stsd.Write(be32(0)) // version+flags and entry count
	stsd.Write(be32(1)) // 1 entry
	entryType := sampleEntryType(t)
	stsd.Write(entryType[:])
	// 8 reserved + 2 data_ref_index + minimal fixed fields.
	stsd.Write(make([]byte, 6))    // reserved
	stsd.Write([]byte{0x00, 0x01}) // data_reference_index
	stsd.Write(make([]byte, 16))   // reserved/pre-defined + predefined
	stsd.Write(be32(320))          // width
	stsd.Write(be32(240))          // height
	boxBytes(&stbl, mp4.BoxTypeStsd(), stsd.Bytes())

	// stts: all samples at a constant delta (1000/timescale placeholder = 1).
	var stts bytes.Buffer
	stts.Write(be32(0))
	stts.Write(be32(1)) // entry count
	stts.Write(be32(uint32(len(samples))))
	stts.Write(be32(1)) // sample delta
	boxBytes(&stbl, mp4.BoxTypeStts(), stts.Bytes())

	// stsc: one run, samples-per-chunk = all samples in one chunk.
	var stsc bytes.Buffer
	stsc.Write(be32(0))
	stsc.Write(be32(1))
	stsc.Write(be32(1)) // first chunk
	stsc.Write(be32(uint32(len(samples))))
	stsc.Write(be32(1)) // sample description index
	boxBytes(&stbl, mp4.BoxTypeStsc(), stsc.Bytes())

	// stsz: per-sample sizes.
	var stsz bytes.Buffer
	stsz.Write(be32(0))
	stsz.Write(be32(0)) // sample_size 0 => entries carry sizes
	stsz.Write(be32(uint32(len(samples))))
	for _, s := range samples {
		stsz.Write(be32(s.size))
	}
	boxBytes(&stbl, mp4.BoxTypeStsz(), stsz.Bytes())

	// stco: single chunk at offset 0 (relative payload indexing is resolved
	// by VerifyRoundTrip against the mdat box directly).
	var stco bytes.Buffer
	stco.Write(be32(0))
	stco.Write(be32(1))
	stco.Write(be32(0))
	boxBytes(&stbl, mp4.BoxTypeStco(), stco.Bytes())

	return stbl.Bytes()
}

func sampleEntryType(t *track.Track) mp4.BoxType {
	switch t.Codec.CodecID {
	case "V_VP9":
		return mp4.StrToBoxType("vp09")
	case "V_VP8":
		return mp4.StrToBoxType("vp08")
	case "A_OPUS":
		return mp4.StrToBoxType("Opus")
	case "A_VORBIS":
		return mp4.StrToBoxType("vrbs")
	default:
		return mp4.StrToBoxType("text")
	}
}
