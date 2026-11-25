package fixture

import "encoding/binary"

type MP4Spec struct {
	Elst       []ElstEntrySpec
	Keyframes  []uint32
	NoStts     bool
	NoStsz     bool
	ZeroStss   bool
	BadStszLen bool
}

func ValidSpec() MP4Spec {
	return MP4Spec{
		Elst: []ElstEntrySpec{{
			Duration: uint32(EditEndTick - EditStartTick), MediaTime: int32(EditStartTick), RateInt: 1,
		}},
		Keyframes: []uint32{1, 5, 9},
	}
}

func BuildValidMP4(spec MP4Spec) []byte {
	sizes := make([]uint32, SampleCount)
	mdatPayload := make([]byte, 0, int(SampleCount*48))
	for i := range sizes {
		sizes[i] = uint32(40 + i%8)
		frame := make([]byte, sizes[i])
		for j := range frame {
			frame[j] = byte(i*17 + j*3)
		}
		mdatPayload = append(mdatPayload, frame...)
	}
	ftyp := FtypBox()
	mdat := Box("mdat", mdatPayload)
	chunkOffset := uint32(len(ftyp) + 8) // one mdat box header

	stblChildren := [][]byte{
		SttsBox(SampleCount, FrameDelta),
		CttsBox(SampleCount, int32(StartTimestamp)),
	}
	switch {
	case spec.ZeroStss:
		stblChildren = append(stblChildren, StssBox())
	case len(spec.Keyframes) > 0:
		stblChildren = append(stblChildren, StssBox(spec.Keyframes...))
	}
	stblChildren = append(stblChildren, StcoBox(chunkOffset))
	if spec.BadStszLen {
		stblChildren = append(stblChildren, truncatedStszBox(SampleCount, sizes))
	} else {
		stblChildren = append(stblChildren, StszBox(0, sizes...))
	}
	stbl := Box("stbl", stblChildren...)
	minf := Box("minf", stbl)
	mdia := Box("mdia", MdhdBox(Timescale, 72_000), HdlrBox(), minf)

	var trakChildren [][]byte
	trakChildren = append(trakChildren, TkhdBox(1, 54_000))
	if len(spec.Elst) > 0 {
		trakChildren = append(trakChildren, Box("edts", ElstBox(spec.Elst...)))
	}
	trakChildren = append(trakChildren, mdia)
	trak := Box("trak", trakChildren...)
	moov := Box("moov", MvhdBox(Timescale, 54_000), trak)

	out := append([]byte(nil), ftyp...)
	out = append(out, mdat...)
	out = append(out, moov...)
	return out
}

func truncatedStszBox(count uint32, sizes []uint32) []byte {
	full := StszBox(0, sizes...)
	// Remove the final four-byte sample size while retaining the declared
	// entry count, producing a truncated sample-size table.
	return full[:len(full)-4]
}

func BuildNoKeyframeMP4() []byte {
	s := ValidSpec()
	s.Keyframes = nil
	s.ZeroStss = true
	return BuildValidMP4(s)
}

func BuildBadEditMP4(kind string) []byte {
	s := ValidSpec()
	switch kind {
	case "empty":
		s.Elst = []ElstEntrySpec{{Duration: 36_000, MediaTime: -1, RateInt: 1}}
	case "oob":
		s.Elst = []ElstEntrySpec{{Duration: 36_000, MediaTime: 999_999, RateInt: 1}}
	case "reverse":
		s.Elst = []ElstEntrySpec{{Duration: 36_000, MediaTime: int32(EditStartTick), RateInt: -1}}
	}
	return BuildValidMP4(s)
}

func BuildCorruptMP4() []byte {
	// Declared box size 1 is smaller than the 8-byte box header.
	b := []byte{0, 0, 0, 1, 'f', 't', 'y', 'p'}
	return append(b, make([]byte, 64)...)
}

// Uint32BE is a small test/helper utility retained for fixture assertions.
func Uint32BE(b []byte, offset int) uint32 {
	return binary.BigEndian.Uint32(b[offset : offset+4])
}
