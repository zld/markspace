// Package fixture generates self-contained sample data for the acceptance
// scenarios: normal, truncated and replaced segments, damaged manifests and
// metadata updates. Nothing here depends on external media files.
package fixture

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"
)

// Video geometry used for every generated fragment. The encoded sample is a
// durable synthetic pattern so a replaced segment differs in content but not
// in structure.
const (
	Width   = 64
	Height  = 48
	FPSNum  = 30
	FPSDen  = 1
	PlanePx = Width * Height
)

// SegmentCount is the number of media segments per fixture playlist.
const SegmentCount = 4

// SegmentDurationTicks is 90000/30 per frame, times the frames per segment.
const (
	Timescale    = 90000
	FrameTicks   = Timescale / 30
	FramesPerSeg = 30
	SegmentTicks = FrameTicks * FramesPerSeg
	SegmentNanos = int64(SegmentTicks) * 1_000_000_000 / Timescale
)

// Frame builds one deterministic luma plane. variant alters texture so
// same-path replacements and metadata updates produce different digests.
func Frame(index, variant int) []byte {
	buf := make([]byte, PlanePx)
	seed := uint32(index*2654435761 + variant*40503 + 7)
	for y := 0; y < Height; y++ {
		for x := 0; x < Width; x++ {
			v := (x*3 + y*2 + index*11 + variant*17) & 0xff
			seed = seed*1664525 + 1013904223
			v += int(seed>>24)&7 - 3
			if v < 0 {
				v = 0
			}
			if v > 255 {
				v = 255
			}
			buf[y*Width+x] = byte(v)
		}
	}
	return buf
}

// BuildSegment writes a structurally valid fMP4 fragment holding one track of
// uncompressed frames, plus a small RTP packet stream stored as a private
// metadata box so the RTP dependency is exercised by real data.
func BuildSegment(index, variant int) ([]byte, error) {
	ftyp := box("ftyp", []byte("iso6\x00\x00\x02\x00iso6mp41"))
	trex := box("trex", concat(fullbox(0), u32(1), u32(1), u32(FrameTicks), u32(PlanePx), u32(0)))
	mvex := box("mvex", trex)
	// moov carries a trak so a standalone segment is probeable; the sample
	// table is empty and timing comes from the fragment.
	mdhd := box("mdhd", concat(fullbox(0), u32(0), u32(0), u32(Timescale), u32(0), []byte{0x55, 0xc4, 0, 0}))
	hdlr := box("hdlr", concat(fullbox(0), u32(0), []byte("vide"), make([]byte, 12), []byte("VideoHandler\x00")))
	tkhd := box("tkhd", tkhdPayload(1))
	emptyStbl := box("stbl", concat(
		box("stsd", concat(fullbox(0), u32(1), rawSampleEntry(Width, Height))),
		box("stts", concat(fullbox(0), u32(0))),
		box("stsc", concat(fullbox(0), u32(0))),
		box("stsz", concat(fullbox(0), u32(0), u32(0))),
		box("stco", concat(fullbox(0), u32(0))),
	))
	minf := box("minf", emptyStbl)
	mdia := box("mdia", concat(mdhd, hdlr, minf))
	trak := box("trak", concat(tkhd, mdia))
	mvhd := box("mvhd", mvhdPayload())
	moov := box("moov", concat(mvhd, trak, mvex))

	rtpBytes, err := rtpStream(index, variant)
	if err != nil {
		return nil, err
	}
	rtpBox := box("udta", box("alab", box("rtp0", rtpBytes)))

	// The sample data starts after the moof and its 8-byte mdat header; the
	// trun data-offset is relative to the moof, so it is the moof length+8.
	buildMoofWithOffset := func(seq uint32, dataOffset int32) []byte {
		mfhd := box("mfhd", concat(fullbox(0), u32(seq)))
		tfhd := box("tfhd", concat(fullbox(0x020000), u32(1)))
		tfdt := box("tfdt", concat(fullbox(1), u64(uint64(seq-1)*SegmentTicks)))
		trunEntries := make([]byte, 0, FramesPerSeg*8)
		for f := 0; f < FramesPerSeg; f++ {
			trunEntries = append(trunEntries, u32(FrameTicks)...)
			trunEntries = append(trunEntries, u32(PlanePx)...)
		}
		trun := box("trun", concat(fullbox(0x000301), u32(uint32(FramesPerSeg)), u32(uint32(dataOffset)), trunEntries))
		traf := box("traf", concat(tfhd, tfdt, trun))
		return box("moof", concat(mfhd, traf))
	}
	// First build with a placeholder to learn the moof length, then rebuild
	// with the true data offset.
	probeMoof := buildMoofWithOffset(1, 0)
	dataOff := int32(len(probeMoof) + 8)
	mf := buildMoofWithOffset(1, dataOff)
	// Variant 2 keeps the exact same luma frames and changes only container
	// metadata, modelling an encapsulation/metadata update with unchanged
	// picture content.
	frameVariant := variant
	if variant == 2 {
		frameVariant = 0
	}
	var frames []byte
	for f := 0; f < FramesPerSeg; f++ {
		frames = append(frames, Frame(index*FramesPerSeg+f, frameVariant)...)
	}
	mdat := box("mdat", frames)
	out := concat(ftyp, moov, mf, mdat, rtpBox)
	return out, nil
}

func rtpStream(index, variant int) ([]byte, error) {
	// RFC 4571 framing: each packet is prefixed with its 2-byte length so a
	// byte-stream reader can recover packet boundaries.
	var out []byte
	for p := 0; p < 4; p++ {
		hdr := rtpHeader(uint16(index*4+p), uint32(index)*3000+uint32(p)*750, 100)
		payload := []byte{byte(variant), byte(index), byte(p)}
		pkt := append(append([]byte{}, hdr...), payload...)
		out = append(out, byte(len(pkt)>>8), byte(len(pkt)))
		out = append(out, pkt...)
	}
	return out, nil
}

// rtpHeader builds a minimal RTP header (version 2, no padding/extension).
func rtpHeader(seq uint16, ts uint32, pt uint8) []byte {
	b := []byte{0x80, pt, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(b[2:], seq)
	binary.BigEndian.PutUint32(b[4:], ts)
	binary.BigEndian.PutUint32(b[8:], 0xdeadbeef)
	return b
}

func tkhdPayload(trackID uint32) []byte {
	return concat(
		fullbox(0x000007), u32(0), u32(0), u32(trackID), u32(0), u32(0),
		u32(0), u32(0), u32(0), u32(0),
		u32(0x00010000), u32(0), u32(0),
		u32(0), u32(0x00010000), u32(0),
		u32(0), u32(0), u32(0x40000000),
		u32(Width<<16), u32(Height<<16),
	)
}

func mvhdPayload() []byte {
	return concat(
		fullbox(0), u32(0), u32(0), u32(1000), u32(0),
		u32(0x00010000), u32(0x0100), u32(0), u32(0), u32(0),
		u32(0x00010000), u32(0), u32(0),
		u32(0), u32(0x00010000), u32(0),
		u32(0), u32(0), u32(0x40000000),
		u32(0), u32(0), u32(0), u32(0), u32(0), u32(0),
		u32(2),
	)
}

func rawSampleEntry(w, h int) []byte {
	p := make([]byte, 78)
	binary.BigEndian.PutUint32(p[0:], uint32(len(p)))
	copy(p[4:], "raw ")
	binary.BigEndian.PutUint16(p[8+6:], 1)
	binary.BigEndian.PutUint16(p[8+8+16:], uint16(w))
	binary.BigEndian.PutUint16(p[8+8+18:], uint16(h))
	return p
}

// Box exposes the fixture box writer for tests that need to craft damaged
// containers by hand.
func Box(typ string, payload []byte) []byte { return box(typ, payload) }

// FullBox exports the version/flags writer for damaged-container tests.
func FullBox(flags uint32) []byte { return fullbox(flags) }

// U32 and Concat expose the byte helpers for tests.
func U32(v uint32) []byte           { return u32(v) }
func Concat(parts ...[]byte) []byte { return concat(parts...) }

func box(typ string, payload []byte) []byte {
	out := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(out, uint32(8+len(payload)))
	copy(out[4:], typ)
	return append(out, payload...)
}

func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }
func fullbox(flags uint32) []byte {
	return []byte{0, byte(flags >> 16), byte(flags >> 8), byte(flags)}
}
func concat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

var _ = mp4.BoxTypeMoof
var _ = fmt.Sprintf
var _ = json.Marshal
var _ = os.WriteFile
var _ = filepath.Join
