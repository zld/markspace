// Package gen synthesises self-contained fMP4 fragment fixtures: audio PCM
// tracks, variable-frame-rate video, tfdt rollover/rollback cases and
// truncated-mdat corruption. No external media files are referenced.
package gen

import "encoding/binary"

// Box builds one ISO box from a type and payload.
func Box(typ string, payload []byte) []byte {
	buf := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(8+len(payload)))
	copy(buf[4:8], typ)
	copy(buf[8:], payload)
	return buf
}

func fullBox(ver uint8, flags uint32) []byte {
	return []byte{ver, byte(flags >> 16), byte(flags >> 8), byte(flags)}
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// SampleSpec describes one media sample placed in an mdat payload.
type SampleSpec struct {
	PCM      []byte // raw sample bytes (also used as video placeholder)
	Duration uint32 // track timescale ticks
}

// TrackSpec is one traf inside a moof.
type TrackSpec struct {
	TrackID uint32
	BaseT   uint32 // tfdt base media decode time (v0)
	Samples []SampleSpec
}

type patch struct {
	pos int // absolute position in the final fragment buffer
}

// Fragment builds one moof+mdat pair containing one or more tracks. Each
// track's trun carries data-offset, per-sample duration and size.
func Fragment(seq uint32, tracks []TrackSpec) []byte {
	var moofPayload []byte
	// mfhd carries the fragment sequence number and must precede every traf.
	moofPayload = append(moofPayload, Box("mfhd", append(fullBox(0, 0), u32(seq)...))...)
	// patchPos records, per track, the trun data-offset field location inside
	// the assembled moof payload (relative to the moof payload start).
	var patches []patch

	for _, tr := range tracks {
		// trun flags: data-offset(0x1)|sample-duration(0x100)|sample-size(0x200)
		pay := append(fullBox(0, 0x301), u32(uint32(len(tr.Samples)))...)
		pay = append(pay, 0, 0, 0, 0) // data-offset placeholder, patched later
		for _, s := range tr.Samples {
			pay = append(pay, u32(s.Duration)...)
			pay = append(pay, u32(uint32(len(s.PCM)))...)
		}
		trun := Box("trun", pay)

		tfhdBox := Box("tfhd", append(fullBox(0, 0), u32(tr.TrackID)...))
		tfdtBox := Box("tfdt", append(fullBox(0, 0), u32(tr.BaseT)...))
		// Absolute position of the data-offset field once moof is final:
		// moof header(8) + current moofPayload length + traf header(8) +
		// tfhd box + tfdt box + trun header(8) + fullbox(4) + count(4).
		fieldPos := 8 + len(moofPayload) + 8 + len(tfhdBox) + len(tfdtBox) + 8 + 4 + 4
		patches = append(patches, patch{pos: fieldPos})

		var trafPayload []byte
		trafPayload = append(trafPayload, tfhdBox...)
		trafPayload = append(trafPayload, tfdtBox...)
		trafPayload = append(trafPayload, trun...)
		moofPayload = append(moofPayload, Box("traf", trafPayload)...)
	}

	moof := Box("moof", moofPayload)

	// Concatenate sample payloads in track order and build the mdat.
	var mdatPayload []byte
	for _, tr := range tracks {
		for _, s := range tr.Samples {
			mdatPayload = append(mdatPayload, s.PCM...)
		}
	}
	mdat := Box("mdat", mdatPayload)
	data := append(moof, mdat...)

	// trun data offset is relative to the start of the enclosing moof (0).
	mdatPayloadAbs := len(moof) + 8
	cursor := mdatPayloadAbs
	for ti, tr := range tracks {
		binary.BigEndian.PutUint32(data[patches[ti].pos:patches[ti].pos+4], uint32(cursor))
		for _, s := range tr.Samples {
			cursor += len(s.PCM)
		}
	}
	return data
}
