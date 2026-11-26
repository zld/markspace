package media

import (
	"encoding/binary"
	"math"

	"continuity/internal/clock"
)

// CMAFWriter produces minimal but spec-shaped self-contained fMP4/CMAF
// fragments: styp + moof(tfhd/tfdt/trun) + mdat. It exists solely so the
// project can generate its own deterministic fixtures — no external media
// files are referenced.
type CMAFWriter struct {
	Sequence     uint32
	Timescale    uint32
	TrackID      uint32
	BaseDecode   uint64
}

// box wraps a four-cc and payload into an ISO-BMFF length-prefixed box.
func box(cc string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], []byte(cc))
	copy(out[8:], payload)
	return out
}

func fullBox(cc string, version uint8, flags uint32, body []byte) []byte {
	hdr := make([]byte, 4)
	hdr[0] = version
	hdr[1] = byte(flags >> 16)
	hdr[2] = byte(flags >> 8)
	hdr[3] = byte(flags)
	return box(cc, append(hdr, body...))
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// Fragment builds one fragment containing one sample of the given duration
// whose payload is the supplied grayscale frame bytes.
func (w *CMAFWriter) Fragment(sampleDuration uint32, payload []byte) ([]byte, error) {
	if w.Timescale == 0 {
		return nil, clock.New(clock.KindInvalidInput, "cmaf", "timescale required")
	}
	// styp
	styp := box("styp", append([]byte("msdh"),
		append(u32(0), []byte("msdh")...)...))

	// moof > traf > tfhd, tfdt(v1), trun
	tfhdFlags := uint32(0x020000) // default-base-is-moof
	tfhd := fullBox("tfhd", 0, tfhdFlags, u32(w.TrackID))
	tfdt := fullBox("tfdt", 1, 0, u64(w.BaseDecode))
	// trun: data-offset present (0x1), one sample, sample-duration present (0x100)
	trunBody := make([]byte, 8)
	binary.BigEndian.PutUint32(trunBody[0:4], 1) // sample count
	// data offset patched after moof sizing; reserve field at bytes 4..8
	binary.BigEndian.PutUint32(trunBody[4:8], 0)
	trunBody = append(trunBody, u32(sampleDuration)...)
	trun := fullBox("trun", 0, 0x101, trunBody)
	traf := box("traf", append(append(tfhd, tfdt...), trun...))
	mfhd := fullBox("mfhd", 0, 0, u32(w.Sequence))
	moof := box("moof", append(mfhd, traf...))

	mdat := box("mdat", payload)

	// Patch trun data offset: byte distance from moof start to mdat payload.
	dataOffset := len(moof) + 8
	// Field position relative to the moof box. Walking the byte layout:
	//   moof hdr(8) | mfhd(16) | traf hdr(8) | tfhd(16) | tfdt(20) |
	//   trun hdr(8) | fullbox hdr(4) | sample-count(4) | data-offset(4)
	patchFromMoof := 8 + 16 + 8 + 16 + 20 + 8 + 4 + 4
	binary.BigEndian.PutUint32(moof[patchFromMoof:patchFromMoof+4], uint32(dataOffset))

	w.BaseDecode += uint64(sampleDuration)
	w.Sequence++
	return append(append(styp, moof...), mdat...), nil
}

// PadToTargetSize appends deterministic zero padding inside the mdat payload
// so the total fragment approaches the byte count implied by a target
// bitrate and duration. Padding is part of the mdat, not a separate box.
func PadToTargetSize(fragment []byte, target int) []byte {
	if len(fragment) >= target {
		return fragment
	}
	// Grow the mdat box in place: rewrite its length header and insert zeros
	// immediately before the box end.
	mdatOff := len(fragment)
	// Locate mdat (last top-level box).
	off := 0
	for off < len(fragment) {
		size := int(binary.BigEndian.Uint32(fragment[off : off+4]))
		if string(fragment[off+4:off+8]) == "mdat" {
			mdatOff = off
			break
		}
		off += size
	}
	pad := target - len(fragment)
	newSize := int(binary.BigEndian.Uint32(fragment[mdatOff:mdatOff+4])) + pad
	out := make([]byte, 0, len(fragment)+pad)
	out = append(out, fragment[:mdatOff]...)
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr, uint32(newSize))
	copy(hdr[4:8], []byte("mdat"))
	out = append(out, hdr...)
	out = append(out, fragment[mdatOff+8:]...)
	out = append(out, make([]byte, pad)...)
	return out
}

// TargetBytesForBitrate returns container bytes for bps over durationSec,
// rounded to the nearest byte.
func TargetBytesForBitrate(bps int64, durationSec float64) int {
	return int(math.Round(float64(bps) * durationSec / 8))
}
