// Package fixture builds the deterministic in-memory media samples used by
// the unit tests and by the archivecheck demo. Nothing here reads external
// files: every sample is synthesised from code.
package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"
)

// Sample geometry shared with the demo output.
const (
	MP4SampleCount = 100
	MP4SampleSize  = 200
	MP4Timescale   = 25000
	MP4Delta       = 1000
	MP4KeyEvery    = 25
	MP4TrackID     = 1
)

// MP4 returns a structurally valid MP4 with one 25 fps video track, 100
// samples in a single chunk and keyframes every 25 samples. When truncate is
// true the tail of the mdat payload is cut away, leaving a box whose declared
// size runs past the end of the file.
func MP4(truncate bool) []byte {
	payload := make([]byte, MP4SampleCount*MP4SampleSize)
	for i := 0; i < MP4SampleCount; i++ {
		for j := 0; j < MP4SampleSize; j++ {
			payload[i*MP4SampleSize+j] = byte((i*31 + j*7) % 251)
		}
	}
	ftyp := makeBox("ftyp", func() []byte {
		var w boxWriter
		w.raw([]byte{'i', 's', 'o', 'm'})
		w.u32(0x200)
		w.raw([]byte{'i', 's', 'o', 'm', 'i', 's', 'o', '6', 'm', 'p', '4', '1'})
		return w.buf.Bytes()
	}())

	stts := fullBox("stts", 0, 0, func(w *boxWriter) {
		w.u32(1)
		w.u32(MP4SampleCount)
		w.u32(MP4Delta)
	})
	stsc := fullBox("stsc", 0, 0, func(w *boxWriter) {
		w.u32(1)
		w.u32(1)
		w.u32(MP4SampleCount)
		w.u32(1)
	})
	stsz := fullBox("stsz", 0, 0, func(w *boxWriter) {
		w.u32(0)
		w.u32(MP4SampleCount)
		for i := 0; i < MP4SampleCount; i++ {
			w.u32(MP4SampleSize)
		}
	})
	var stco []byte
	stss := fullBox("stss", 0, 0, func(w *boxWriter) {
		var nums []uint32
		for n := 1; n <= MP4SampleCount; n += MP4KeyEvery {
			nums = append(nums, uint32(n))
		}
		w.u32(uint32(len(nums)))
		for _, n := range nums {
			w.u32(n)
		}
	})
	var stbl []byte
	var minf []byte
	hdlr := fullBox("hdlr", 0, 0, func(w *boxWriter) {
		w.u32(0)
		w.raw([]byte("vide"))
		w.raw(make([]byte, 12))
		w.raw([]byte("VideoHandler\x00"))
	})
	mdhd := fullBox("mdhd", 0, 0, func(w *boxWriter) {
		w.u32(0)
		w.u32(0)
		w.u32(MP4Timescale)
		w.u32(MP4SampleCount * MP4Delta)
		w.u32(0x55c40000) // language "und" packed
		w.u32(0)
	})
	var mdia []byte
	tkhd := fullBox("tkhd", 0, 3, func(w *boxWriter) {
		w.u32(0)
		w.u32(0)
		w.u32(MP4TrackID)
		w.u32(0)
		w.u32(MP4SampleCount * MP4Delta * 1000 / MP4Timescale)
		w.raw(make([]byte, 8))
		w.u32(0) // layer + alternate_group
		w.u32(0) // volume + reserved
		matrix := []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
		for _, m := range matrix {
			w.u32(m)
		}
		w.u32(0)
		w.u32(0)
	})
	// stco needs the absolute mdat payload position, which depends on the
	// moov size. The stco box is fixed-size, so a two-pass build converges:
	// the first pass fixes the size, the second writes the real offset.
	var stbl0 []byte
	stbl0 = makeBox("stbl", concat(stts, stsc, stsz, makeBox("stco", make([]byte, 12)), stss))
	sizeHint := len(ftyp) + len(makeBox("moov", makeBox("trak", concat(tkhd, makeBox("mdia", concat(mdhd, hdlr, makeBox("minf", stbl0))))))) + 8
	_ = sizeHint
	mdatStart := int64(sizeHint)
	stco = fullBox("stco", 0, 0, func(w *boxWriter) {
		w.u32(1)
		w.u32(uint32(mdatStart))
	})
	stbl = makeBox("stbl", concat(stts, stsc, stsz, stco, stss))
	minf = makeBox("minf", stbl)
	mdia = makeBox("mdia", concat(mdhd, hdlr, minf))
	trak := makeBox("trak", concat(tkhd, mdia))
	moov := makeBox("moov", trak)
	out := concat(ftyp, moov, makeBox("mdat", payload))
	if truncate {
		out = out[:len(out)-37]
	}
	return out
}

// RTP returns a length-prefixed RTP capture of 40 packets with two sequence
// gaps at the configured positions. Marshalling a fixed packet can only fail
// if the payload is invalid, which is a programming error in this file, so the
// error is surfaced instead of ignored.
func RTP() ([]byte, error) {
	const packets = 40
	var out []byte
	seq := 1000
	for i := 0; i < packets; i++ {
		if i == 12 {
			seq += 3 // three packets lost
		}
		if i == 27 {
			seq += 2 // two more lost
		}
		p := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(seq),
				Timestamp:      uint32(9000 + i*3600),
				SSRC:           0x0badf00d,
			},
			Payload: bytes.Repeat([]byte{byte(i)}, 48),
		}
		raw, err := p.Marshal()
		if err != nil {
			return nil, fmt.Errorf("fixture: marshal RTP packet %d: %w", i, err)
		}
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], uint32(len(raw)))
		out = append(out, prefix[:]...)
		out = append(out, raw...)
		seq++
	}
	return out, nil
}

// SRTRecoverable returns an SRT sample with two recoverable cues around a
// malformed time and a reversed interval.
func SRTRecoverable() []byte {
	return []byte("1\r\n00:00:01,000 --> 00:00:02,500\r\nOpening line\r\nsecond line\r\n\r\n" +
		"2\r\n00:00:03,00X --> 00:00:04,000\r\nbad milliseconds\r\n\r\n" +
		"3\r\n00:00:05,000 --> 00:00:04,000\r\nreversed\r\n\r\n" +
		"4\r\n00:00:06,000 --> 00:00:08,250\r\nClosing line\r\n")
}

// VTTValid returns a WebVTT sample with identifiers, settings and multi-line
// cues.
func VTTValid() []byte {
	return []byte("WEBVTT - archive sample\nKind: captions\n\n" +
		"NOTE generated by archivecheck\n\n" +
		"intro\n00:00:01.000 --> 00:00:03.500 line:0 position:50% align:middle\nFirst line\nSecond line\n\n" +
		"00:00:04.000 --> 00:00:06.000\nTail line\n")
}

// TimecodesValid maps a label to a valid timecode for each supported rate.
func TimecodesValid() map[string]string {
	return map[string]string{
		"24p":       "00:00:10:12",
		"25p":       "00:00:10:12",
		"29.97-ndf": "00:00:10:12",
		"29.97-df":  "00:10:00;00",
		"50p":       "00:00:10:12",
	}
}

// TimecodesInvalid maps a label to a timecode rejected by its rate.
func TimecodesInvalid() map[string]string {
	return map[string]string{
		"negative":     "-00:00:01:00",
		"bad-drop":     "00:01:00;00",
		"frame-over":   "00:00:00:25",
		"minute-range": "00:61:00:00",
	}
}

type boxWriter struct{ buf bytes.Buffer }

func (w *boxWriter) raw(b []byte) { w.buf.Write(b) }
func (w *boxWriter) u32(v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	w.buf.Write(b[:])
}

func fullBox(typ string, version uint8, flags uint32, body func(*boxWriter)) []byte {
	var w boxWriter
	w.buf.WriteByte(version)
	w.raw([]byte{byte(flags >> 16), byte(flags >> 8), byte(flags)})
	body(&w)
	return makeBox(typ, w.buf.Bytes())
}

func makeBox(typ string, payload []byte) []byte {
	var h [8]byte
	binary.BigEndian.PutUint32(h[:4], uint32(8+len(payload)))
	copy(h[4:], typ)
	return concat(h[:], payload)
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
