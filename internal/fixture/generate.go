// Package fixture generates deterministic local media containers and RTP
// event streams. No external sample files are required: every byte is
// synthesized so tests are hermetic and reproducible.
package fixture

import (
	"bytes"
	"encoding/binary"
	"os"
)

// TrackSpec describes one synthetic track. Deltas are per-sample durations
// in the track timescale; Payloads are the raw sample bytes concatenated
// into mdat in track order.
type TrackSpec struct {
	Handler   string
	TrackID   uint32
	Timescale uint32
	Deltas    []uint32
	Payloads  [][]byte
}

func box(typ string, payload []byte) []byte {
	if len(typ) != 4 {
		panic("box type must be 4 bytes: " + typ)
	}
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func fullBox(version byte, flags uint32, payload []byte) []byte {
	head := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return append(head, payload...)
}

func putU32(b []byte, v uint32) { binary.BigEndian.PutUint32(b, v) }

// BuildMP4 serializes a minimal but structurally valid ISO-BMFF byte stream
// containing ftyp, moov (with full stbl tables) and mdat.
func BuildMP4(movieTimescale uint32, specs []TrackSpec) []byte {
	var mdat bytes.Buffer
	var traks bytes.Buffer
	var totalDurationNS int64

	for _, spec := range specs {
		// Lay samples out in mdat and remember per-sample file offsets.
		type chunk struct {
			offset int64
			count  uint32
		}
		var chunks []chunk
		offsets := make([]uint32, 0, len(spec.Deltas))
		for i := range spec.Deltas {
			payload := []byte{byte(i), byte(spec.TrackID), 0xAA, 0x55}
			if i < len(spec.Payloads) && len(spec.Payloads[i]) > 0 {
				payload = spec.Payloads[i]
			}
			if i == 0 {
				chunks = append(chunks, chunk{offset: int64(mdat.Len()), count: 1})
			} else {
				chunks[len(chunks)-1].count++
			}
			offsets = append(offsets, uint32(mdat.Len()))
			mdat.Write(payload)
		}

		var dur uint32
		for _, d := range spec.Deltas {
			dur += d
		}
		var durNS int64
		if spec.Timescale != 0 {
			durNS = int64(dur) * 1_000_000_000 / int64(spec.Timescale)
		}
		if durNS > totalDurationNS {
			totalDurationNS = durNS
		}

		// mdhd (version 0, 20-byte payload).
		mdhdPayload := make([]byte, 20)
		putU32(mdhdPayload[8:12], spec.Timescale)
		putU32(mdhdPayload[12:16], dur)
		mdhd := box("mdhd", fullBox(0, 0, mdhdPayload))

		// hdlr.
		var hdlrPayload bytes.Buffer
		hdlrPayload.Write(make([]byte, 4)) // pre_defined
		hdlrPayload.WriteString(fix4(spec.Handler))
		hdlrPayload.Write(make([]byte, 12)) // reserved
		hdlrPayload.WriteByte(0)            // empty name
		hdlr := box("hdlr", fullBox(0, 0, hdlrPayload.Bytes()))

		// stts
		sttsPayload := make([]byte, 4+8*len(spec.Deltas))
		putU32(sttsPayload[0:4], uint32(len(spec.Deltas)))
		for i, d := range spec.Deltas {
			putU32(sttsPayload[4+i*8:8+i*8], 1)
			putU32(sttsPayload[8+i*8:12+i*8], d)
		}
		stts := box("stts", fullBox(0, 0, sttsPayload))

		// stsc: every chunk holds all samples in this one-chunk fixture.
		stscPayload := make([]byte, 16)
		putU32(stscPayload[0:4], 1)
		putU32(stscPayload[4:8], 1) // first_chunk (1-based)
		putU32(stscPayload[8:12], uint32(len(spec.Deltas)))
		stsc := box("stsc", fullBox(0, 0, stscPayload))

		// stsz
		stszPayload := make([]byte, 8+4*len(offsets))
		putU32(stszPayload[4:8], uint32(len(offsets)))
		for i, off := range offsets {
			size := uint32(4)
			if i < len(spec.Payloads) && len(spec.Payloads[i]) > 0 {
				size = uint32(len(spec.Payloads[i]))
			}
			putU32(stszPayload[8+i*4:12+i*4], size)
			_ = off
		}
		stsz := box("stsz", fullBox(0, 0, stszPayload))

		// stco: offsets are mdat-relative for now, fixed up after layout.
		stcoPayload := make([]byte, 8)
		putU32(stcoPayload[0:4], 1)
		putU32(stcoPayload[4:8], uint32(chunks[0].offset))
		stco := box("stco", fullBox(0, 0, stcoPayload))

		stbl := box("stbl", concat(stts, stsc, stsz, stco))
		minf := box("minf", stbl)
		mdia := box("mdia", concat(mdhd, hdlr, minf))

		// tkhd (version 0, 80-byte payload).
		tkhdPayload := make([]byte, 80)
		putU32(tkhdPayload[8:12], spec.TrackID)
		putU32(tkhdPayload[20:24], dur)
		tkhd := box("tkhd", fullBox(0, 0x7, tkhdPayload))

		traks.Write(box("trak", concat(tkhd, mdia)))
	}

	movieDur := uint32(0)
	if movieTimescale != 0 && totalDurationNS > 0 {
		movieDur = uint32(totalDurationNS * int64(movieTimescale) / 1_000_000_000)
	}
	mvhdPayload := make([]byte, 96)
	putU32(mvhdPayload[8:12], movieTimescale)
	putU32(mvhdPayload[12:16], movieDur)
	binary.BigEndian.PutUint32(mvhdPayload[16:20], 0x00010000) // rate
	mvhdPayload[20] = 0x01                                      // volume
	mvhd := box("mvhd", fullBox(0, 0, mvhdPayload))

	moov := box("moov", concat(mvhd, traks.Bytes()))
	ftyp := box("ftyp", concat(fix4b("isom"), u32(0x200), fix4b("isom"), fix4b("mp42")))

	var out bytes.Buffer
	out.Write(ftyp)
	mdatHeaderLen := 8
	mdatOffset := out.Len() + len(moov)
	out.Write(moov)
	out.Write(box("mdat", mdat.Bytes()))

	// Fix up stco absolute file offsets (mdat payload starts after header).
	fixed := out.Bytes()
	payloadBase := uint32(mdatOffset + mdatHeaderLen)
	fixStco(fixed, payloadBase)
	return fixed
}

func fixStco(buf []byte, base uint32) {
	// Rewrite every stco chunk-offset entry to absolute positions.
	marker := []byte("stco")
	for i := 0; i+8 <= len(buf); {
		idx := bytes.Index(buf[i:], marker)
		if idx < 0 {
			return
		}
		pos := i + idx
		size := binary.BigEndian.Uint32(buf[pos-4 : pos])
		payloadStart := pos + 4 + 4 // skip type and fullbox version/flags
		count := binary.BigEndian.Uint32(buf[payloadStart : payloadStart+4])
		for k := uint32(0); k < count; k++ {
			ep := payloadStart + 4 + int(k)*4
			rel := binary.BigEndian.Uint32(buf[ep : ep+4])
			binary.BigEndian.PutUint32(buf[ep:ep+4], base+rel)
		}
		i = (pos - 4) + int(size)
	}
}

// WriteMP4 builds and persists a container, returning the written bytes.
func WriteMP4(path string, movieTimescale uint32, specs []TrackSpec) ([]byte, error) {
	data := BuildMP4(movieTimescale, specs)
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil, err
	}
	return data, nil
}

// Truncate keeps only the first n bytes of a file (corrupt/truncated case).
func Truncate(path string, n int64) error {
	return os.Truncate(path, n)
}

func concat(parts ...[]byte) []byte {
	var b bytes.Buffer
	for _, p := range parts {
		b.Write(p)
	}
	return b.Bytes()
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	putU32(b, v)
	return b
}

func fix4b(s string) []byte {
	if len(s) >= 4 {
		return []byte(s[:4])
	}
	return append([]byte(s), make([]byte, 4-len(s))...)
}

func fix4(s string) string {
	return string(fix4b(s))
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}
