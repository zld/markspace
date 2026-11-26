// Package fixture generates deterministic local capture material for
// tests and the end-to-end verify command: length-prefixed RTP streams
// (Opus/AAC) and minimal but valid MP4 audio tracks, plus deliberate
// damage and truncation variants.
package fixture

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"
	"github.com/pion/rtp"
)

// WriteRTPFile serializes packets as 16-bit big-endian length prefix plus
// raw RTP packet, the on-disk framing consumed by ingest.StreamRTP.
func WriteRTPFile(path string, packets [][]byte) error {
	var buf bytes.Buffer
	var lenBuf [2]byte
	for _, p := range packets {
		binary.BigEndian.PutUint16(lenBuf[:], uint16(len(p)))
		buf.Write(lenBuf[:])
		buf.Write(p)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// RTPPacket builds one RTP packet with an explicit sequence number.
func RTPPacket(ssrc uint32, seq uint16, ts uint32, pt uint8, payload []byte) []byte {
	pkt := rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    pt,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           ssrc,
			Marker:         true,
		},
		Payload: payload,
	}
	raw, err := pkt.Marshal()
	if err != nil {
		panic(err)
	}
	return raw
}

// OpusPayload returns an Opus packet whose TOC selects config (frame
// duration/band) and code (frame packing). dataLen is the total payload
// size including TOC.
func OpusPayload(config int, code byte, dataLen int) []byte {
	if dataLen < 1 {
		dataLen = 1
	}
	out := make([]byte, dataLen)
	out[0] = byte(config<<3) | (code & 0x03)
	for i := 1; i < dataLen; i++ {
		out[i] = byte(i*7 + config)
	}
	return out
}

// AACPayload wraps one or more AAC access units in RFC 3640 AAC-hbr
// framing: AU-headers-length, one 16-bit AU header per AU (13-bit size),
// then the AU bytes.
func AACPayload(auSizes ...int) []byte {
	var buf bytes.Buffer
	headerBits := uint16(len(auSizes) * 16)
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], headerBits)
	buf.Write(l[:])
	for _, sz := range auSizes {
		var h [2]byte
		binary.BigEndian.PutUint16(h[:], uint16(sz<<3))
		buf.Write(h[:])
	}
	for i, sz := range auSizes {
		for b := 0; b < sz; b++ {
			buf.WriteByte(byte(i*11 + b))
		}
	}
	return buf.Bytes()
}

// WriteMP4File writes a minimal single-track audio MP4 with an explicit
// stts/stsz/stco table and an mdat holding sampleCount dummy samples.
func WriteMP4File(path string, timescale uint32, sampleDelta uint32, sampleSizes []uint32) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := mp4.NewWriter(f)

	// ftyp
	ftyp := &mp4.Ftyp{MajorBrand: [4]byte{'i', 's', 'o', 'm'}, MinorVersion: 0x200}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, ftyp, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	// moov/trak/mdia/minf/stbl
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	if err := writeMvhd(w); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if err := writeTkhd(w); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if err := writeMdhd(w, timescale, sampleDelta*uint32(len(sampleSizes))); err != nil {
		return err
	}
	if err := writeHdlr(w); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if err := writeSmhd(w); err != nil {
		return err
	}
	if err := writeDinf(w); err != nil {
		return err
	}
	if err := writeStbl(w, timescale, sampleDelta, sampleSizes); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // minf
		return err
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return err
	}
	if _, err := w.EndBox(); err != nil { // trak
		return err
	}
	if _, err := w.EndBox(); err != nil { // moov
		return err
	}

	// mdat with placeholder offset patched via stco computed below.
	mdatStart, err := f.Seek(0, os.SEEK_CUR)
	if err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return err
	}
	for i, sz := range sampleSizes {
		payload := make([]byte, sz)
		for b := range payload {
			payload[b] = byte(i*13 + b)
		}
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	// Patch stco to the first sample's offset (immediately after mdat hdr).
	chunkOffset := uint32(mdatStart) + 8
	return patchStco(f, chunkOffset)
}
