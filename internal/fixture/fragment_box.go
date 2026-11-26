package fixture

import (
	"bytes"
	"encoding/binary"

	"github.com/abema/go-mp4"
)

const (
	videoTimescale = 30_000
	audioTimescale = 48_000
	videoSamples   = 30
	audioSamples   = 50
)

type fragmentTiming struct {
	Sequence  uint32
	VideoTFDT uint32
	AudioTFDT uint32
	BadTFDT   bool
}

func mfhd(seq uint32) []byte {
	var payload bytes.Buffer
	if _, err := mp4.Marshal(&payload, &mp4.Mfhd{SequenceNumber: seq}, mp4.Context{}); err != nil {
		panic(err)
	}
	return box("mfhd", payload.Bytes())
}

func tfhd(trackID, defaultFlags uint32) []byte {
	// 0x000020 = default-sample-flags-present.
	return fullBox("tfhd", 0, 0x000020, u32(trackID), u32(defaultFlags))
}

func tfdt(v uint32) []byte {
	return fullBox("tfdt", 0, 0, u32(v))
}

func trun(count uint32, dataOffset int32, firstFlags uint32, durations, sizes []uint32) []byte {
	// data-offset(0x1), first-sample-flags(0x4), duration(0x100), size(0x200).
	const flags = 0x000001 | 0x000004 | 0x000100 | 0x000200
	p := concat(u32(count), u32(uint32(dataOffset)), u32(firstFlags))
	for i := uint32(0); i < count; i++ {
		p = append(p, u32(durations[i])...)
		p = append(p, u32(sizes[i])...)
	}
	return fullBox("trun", 0, flags, p)
}

func videoSampleArrays() ([]uint32, []uint32, []byte) {
	durations := make([]uint32, videoSamples)
	sizes := make([]uint32, videoSamples)
	payload := make([]byte, 0, videoSamples*16)
	for i := range durations {
		durations[i] = 1000 // 30 * 1000 / 30000 = 1 second
		sizes[i] = 16
		payload = append(payload, make([]byte, 16)...)
	}
	return durations, sizes, payload
}

func audioSampleArrays() ([]uint32, []uint32, []byte) {
	durations := make([]uint32, audioSamples)
	sizes := make([]uint32, audioSamples)
	payload := make([]byte, 0, audioSamples*8)
	for i := range durations {
		durations[i] = 960 // 50 * 960 / 48000 = 1 second
		sizes[i] = 8
		payload = append(payload, make([]byte, 8)...)
	}
	return durations, sizes, payload
}

// Fragment builds a complete moof+mdat fragment. Faults alter only timing or
// sequence fields; box bounds remain valid unless Truncated is set.
func Fragment(seq uint32, videoTFDT, audioTFDT uint32) []byte {
	vd, vs, vpayload := videoSampleArrays()
	ad, as, apayload := audioSampleArrays()

	keyFlags := uint32(0x02000000)

	videoTrun0 := trun(videoSamples, 0, keyFlags, vd, vs)
	audioTrun0 := trun(audioSamples, 0, 0, ad, as)
	videoTraf0 := box("traf", tfhd(1, 0x00010000), tfdt(videoTFDT), videoTrun0)
	audioTraf0 := box("traf", tfhd(2, 0), tfdt(audioTFDT), audioTrun0)
	moof0 := box("moof", mfhd(seq), videoTraf0, audioTraf0)
	moofLen := int32(len(moof0))

	videoTrun := trun(videoSamples, moofLen+8, keyFlags, vd, vs)
	audioTrun := trun(audioSamples, moofLen+8+int32(len(vpayload)), 0, ad, as)
	videoTraf := box("traf", tfhd(1, 0x00010000), tfdt(videoTFDT), videoTrun)
	audioTraf := box("traf", tfhd(2, 0), tfdt(audioTFDT), audioTrun)
	moof := box("moof", mfhd(seq), videoTraf, audioTraf)
	if len(moof) != int(moofLen) {
		panic("moof length changed while setting trun data offsets")
	}
	mdat := box("mdat", concat(vpayload, apayload))
	return concat(moof, mdat)
}

// TruncatedFragment returns a valid moof followed by an out-of-bounds mdat.
func TruncatedFragment(seq uint32) []byte {
	good := Fragment(seq, 6*30_000, 6*48_000)
	// Keep only the moof prefix, then write an mdat header promising far more.
	moofEnd := locateMoofEnd(good)
	out := append([]byte{}, good[:moofEnd]...)
	header := make([]byte, 8)
	binary.BigEndian.PutUint32(header[0:4], 4096)
	copy(header[4:8], "mdat")
	out = append(out, header...)
	out = append(out, 0, 1, 2, 3) // only a few payload bytes
	return out
}

func locateMoofEnd(b []byte) int {
	if len(b) < 8 {
		return len(b)
	}
	if string(b[4:8]) != "moof" {
		return len(b)
	}
	return int(binary.BigEndian.Uint32(b[0:4]))
}
