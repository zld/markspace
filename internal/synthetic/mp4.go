// Package synthetic generates deterministic local test assets: CMAF init and
// media fragments, RTP captures and client event logs. Nothing is fetched from
// the network and every byte is derived from an explicit seed.
package synthetic

import (
	"bytes"
	"encoding/binary"
	"io"

	"github.com/abema/go-mp4"
)

// boxBuilder writes length-prefixed MP4 boxes into an in-memory buffer.
type boxBuilder struct{ buf bytes.Buffer }

func (b *boxBuilder) raw(p []byte) { b.buf.Write(p) }

// start writes a box header with a placeholder size and returns the offset
// of the payload so End can patch the 32-bit size.
func (b *boxBuilder) start(boxType string) int {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr, 0)
	copy(hdr[4:], []byte(boxType))
	b.buf.Write(hdr)
	return b.buf.Len()
}

func (b *boxBuilder) end(payloadStart int) {
	size := uint32(b.buf.Len() - payloadStart + 8)
	binary.BigEndian.PutUint32(b.buf.Bytes()[payloadStart-8:], size)
}

// fullHeader writes a version/flags header for a FullBox.
func fullHeader(version byte, flags uint32) []byte {
	h := make([]byte, 4)
	h[0] = version
	h[1] = byte(flags >> 16)
	h[2] = byte(flags >> 8)
	h[3] = byte(flags)
	return h
}

func writeU32(w io.Writer, v uint32) { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); w.Write(b) }
func writeU64(w io.Writer, v uint64) { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); w.Write(b) }
func writeI32(w io.Writer, v int32)  { writeU32(w, uint32(v)) }

// VideoSample is one synthetic VFR video frame payload descriptor.
type VideoSample struct {
	Duration uint32 // timescale units (90000)
	CTS      int32  // composition time offset in timescale units
	Width    uint16
	Height   uint16
	Seed     byte
}

// BuildVideoFragment writes styp + moof(tfhd,tfdt,trun) + mdat for one track.
// Sample payloads carry a deterministic header so evidence rendering can
// reconstruct frame dimensions without a real codec.
func BuildVideoFragment(trackID, sequence uint32, baseDecodeTime uint64, samples []VideoSample) []byte {
	var b boxBuilder
	// styp
	ps := b.start("styp")
	b.raw([]byte("msdh")) // major brand
	writeU32(&b.buf, 0)   // minor version
	b.raw([]byte("msdh"))
	b.raw([]byte("msix"))
	b.end(ps)

	// moof
	moofStart := b.start("moof")
	mf := b.start("mfhd")
	b.raw(fullHeader(0, 0))
	writeU32(&b.buf, sequence)
	b.end(mf)

	trafStart := b.start("traf")
	tfhd := b.start("tfhd")
	b.raw(fullHeader(0, mp4.TfhdDefaultBaseIsMoof))
	writeU32(&b.buf, trackID)
	b.end(tfhd)

	tfdt := b.start("tfdt")
	b.raw(fullHeader(1, 0))
	writeU64(&b.buf, baseDecodeTime)
	b.end(tfdt)

	trun := b.start("trun")
	// flags: data-offset(0x1) | sample-duration(0x100) | sample-size(0x200) | cts(0x800)
	b.raw(fullHeader(1, 0x000b01))
	writeU32(&b.buf, uint32(len(samples)))
	// data offset patched after mdat layout is known; reserve position.
	dataOffPos := b.buf.Len()
	writeU32(&b.buf, 0)
	var payloadSizes []uint32
	var payloads [][]byte
	for _, s := range samples {
		writeU32(&b.buf, s.Duration)
		payload := framePayload(s)
		payloads = append(payloads, payload)
		payloadSizes = append(payloadSizes, uint32(len(payload)))
		writeU32(&b.buf, uint32(len(payload)))
		writeI32(&b.buf, s.CTS)
	}
	b.end(trun)
	b.end(trafStart)
	b.end(moofStart)

	// mdat
	mdatStart := b.start("mdat")
	payloadStart := b.buf.Len()
	for _, p := range payloads {
		b.raw(p)
	}
	b.end(mdatStart)

	// Patch trun data offset: bytes from moof start to first sample payload.
	dataOffset := uint32(payloadStart - (moofStart - 8))
	binary.BigEndian.PutUint32(b.buf.Bytes()[dataOffPos:], dataOffset)
	return b.buf.Bytes()
}

// framePayload: 8-byte deterministic header + deterministic pixel seed bytes.
// Header: 'F' 'R' 'M' 0, width(u16), height(u16), seed byte; no codec needed.
func framePayload(s VideoSample) []byte {
	header := make([]byte, 8)
	header[0] = 'F'
	header[1] = 'R'
	header[2] = 'M'
	header[3] = 0
	binary.BigEndian.PutUint16(header[4:], s.Width)
	binary.BigEndian.PutUint16(header[6:], s.Height)
	pixels := make([]byte, int(s.Width)*int(s.Height)*3)
	for i := range pixels {
		pixels[i] = s.Seed + byte(i*7)
	}
	return append(header, pixels...)
}
