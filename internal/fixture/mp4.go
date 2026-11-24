// Package fixture generates all sample data in-process: DASH/HLS manifests,
// fragmented MP4 segments, length-prefixed RTP captures and their damaged
// variants. No external files or network resources are ever referenced.
package fixture

import (
	"encoding/binary"
)

// box writes a four-character-code box with the given payload.
func box(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], []byte(typ))
	copy(out[8:], payload)
	return out
}

// fullBox writes version/flags then payload inside a box of typ.
func fullBox(typ string, version byte, flags uint32, payload []byte) []byte {
	hdr := make([]byte, 4)
	hdr[0] = version
	hdr[1] = byte(flags >> 16)
	hdr[2] = byte(flags >> 8)
	hdr[3] = byte(flags)
	return box(typ, append(hdr, payload...))
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

func u16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

// FrameSpec describes one synthetic sample.
type FrameSpec struct {
	Payload  []byte
	Duration uint32 // in track ticks
	KeyFrame bool
}

// medsBox builds the fixture descriptor carrying timescale and kind.
func medsBox(timescale uint32, kind byte) []byte {
	payload := make([]byte, 9)
	binary.BigEndian.PutUint32(payload[4:8], timescale)
	payload[8] = kind
	return box("meds", payload)
}

// stypBox is the segment-type box.
func stypBox() []byte {
	payload := make([]byte, 8)
	copy(payload[0:4], []byte("msdh"))
	binary.BigEndian.PutUint32(payload[4:8], 0)
	return box("styp", payload)
}

// moofBox builds moof>traf{tfhd,tfdt(v1),trun} for one fragment.
func moofBox(trackID uint32, baseTick uint64, frames []FrameSpec) []byte {
	tfhd := fullBox("tfhd", 0, 0x020000, u32(trackID))
	tfdt := fullBox("tfdt", 1, 0, u64(baseTick))
	trunPayload := u32(uint32(len(frames)))
	for _, f := range frames {
		trunPayload = append(trunPayload, u32(f.Duration)...)
		trunPayload = append(trunPayload, u32(uint32(len(f.Payload)))...)
		var flags uint32
		if f.KeyFrame {
			flags = 0x02000000 // sample_depends_on = 2 (non-sample / keyframe)
		}
		trunPayload = append(trunPayload, u32(flags)...)
	}
	trun := fullBox("trun", 0, 0x000700, trunPayload) // d,dur,size,flags present
	traf := box("traf", append(append(tfhd, tfdt...), trun...))
	return box("moof", traf)
}

// mdatBox concatenates frame payloads.
func mdatBox(frames []FrameSpec) []byte {
	var data []byte
	for _, f := range frames {
		data = append(data, f.Payload...)
	}
	return box("mdat", data)
}

// BuildSegment produces one self-describing fragmented MP4 segment.
func BuildSegment(timescale uint32, kind byte, trackID uint32, baseTick uint64, frames []FrameSpec) []byte {
	var out []byte
	out = append(out, stypBox()...)
	out = append(out, medsBox(timescale, kind)...)
	out = append(out, moofBox(trackID, baseTick, frames)...)
	out = append(out, mdatBox(frames)...)
	return out
}
