package testdata

import (
	"os"
	"path/filepath"
)

// MP4Spec describes one deterministic synthetic MP4 fixture.
type MP4Spec struct {
	Seed       int64
	Width      int
	Height     int
	Timescale  uint32 // video timescale
	Audio      AudioContent
	Frames     []FrameContent
	VideoPTS   []int64
	VideoDTS   []int64
	VideoDur   []int64
	StartNanos int64 // non-zero media start, encoded via edit list
	Rotation   int   // 0/90/180/270 degrees
	Crop       [4]int64
	Colors     []ColorSpec
}

// ColorSpec is one nclx declaration; multiple entries create a conflict.
type ColorSpec struct {
	Primaries uint16
	Transfer  uint16
	Matrix    uint16
	FullRange bool
}

// WriteMP4 writes the synthetic MP4 with one video and one audio track.
// Layout: ftyp, moov, video mdat, audio mdat (faststart). Sample offsets are
// computed for the pre-moov layout, then shifted by the moov byte length.
func WriteMP4(path string, spec MP4Spec) error {
	ftypPayload := append([]byte("isom"), 0, 0, 0, 0)
	ftypPayload = append(ftypPayload, []byte("isom")...)
	ftypPayload = append(ftypPayload, []byte("iso2")...)
	ftypPayload = append(ftypPayload, []byte("mp41")...)
	ftyp := box("ftyp", ftypPayload)

	// First pass: offsets assume the old ftyp,mdat,mdat layout.
	var preLen int
	vSizes := make([]uint32, len(spec.Frames))
	vOff := make([]uint32, len(spec.Frames))
	var vPayload []byte
	preLen = len(ftyp)
	for i, f := range spec.Frames {
		vOff[i] = uint32(preLen + 8 + len(vPayload))
		vSizes[i] = uint32(len(f.Luma))
		vPayload = append(vPayload, f.Luma...)
	}
	vMdat := box("mdat", vPayload)

	const group = 1024
	groups := len(spec.Audio.PCM) / group
	aSizes := make([]uint32, groups)
	aOff := make([]uint32, groups)
	var aPayload []byte
	aBase := preLen + len(vMdat)
	for g := 0; g < groups; g++ {
		aOff[g] = uint32(aBase + 8 + len(aPayload))
		aSizes[g] = group * 2
		aPayload = append(aPayload, pcmBytes(spec.Audio.PCM[g*group:(g+1)*group])...)
	}
	aMdat := box("mdat", aPayload)

	vTrak := buildVideoTrak(spec, vSizes, vOff)
	aTrak := buildAudioTrak(spec.Audio, groups, aSizes, aOff)

	mvhdPayload := make([]byte, 96)
	putU32(mvhdPayload[8:], 1000) // movie timescale (v0 offset 8)
	dur := uint64(groups*group) * 1000 / uint64(spec.Audio.Rate)
	putU32(mvhdPayload[12:], uint32(dur)) // duration (v0 offset 12)
	moov := box("moov", concat([][]byte{
		box("mvhd", fullbox(0, 0, mvhdPayload)), vTrak, aTrak,
	}))

	// Second pass: moov now sits between ftyp and the mdats, so every chunk
	// offset shifts forward by len(moov).
	shift := uint32(len(moov))
	for i := range vOff {
		vOff[i] += shift
	}
	for i := range aOff {
		aOff[i] += shift
	}
	vTrak = buildVideoTrak(spec, vSizes, vOff)
	aTrak = buildAudioTrak(spec.Audio, groups, aSizes, aOff)
	moov = box("moov", concat([][]byte{
		box("mvhd", fullbox(0, 0, mvhdPayload)), vTrak, aTrak,
	}))

	file := concat([][]byte{ftyp, moov, vMdat, aMdat})
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, file, 0o644)
}

func buildVideoTrak(spec MP4Spec, sizes, off []uint32) []byte {
	deltas := make([]uint32, len(spec.Frames))
	var durationUnits uint64
	for i := range spec.Frames {
		deltas[i] = uint32(spec.VideoDur[i] * int64(spec.Timescale) / 1_000_000_000)
		if deltas[i] == 0 {
			deltas[i] = 1
		}
		durationUnits += uint64(deltas[i])
	}
	cttsOff := make([]int64, len(spec.Frames))
	for i := range cttsOff {
		// Composition offset is expressed in track timescale units.
		cttsOff[i] = (spec.VideoPTS[i] - spec.VideoDTS[i]) *
			int64(spec.Timescale) / 1_000_000_000
	}
	var keys []uint32
	for i, f := range spec.Frames {
		if f.Key {
			keys = append(keys, uint32(i+1))
		}
	}
	children := [][]byte{
		stsd(videoEntry(spec)), sttsBox(deltas), stscBox(len(sizes)),
		stszBox(sizes), stcoBox(off),
	}
	if c := cttsBox(cttsOff); c != nil {
		children = append(children, c)
	}
	children = append(children, stssBox(keys))
	stbl := box("stbl", concat(children))
	minf := box("minf", concat([][]byte{
		box("vmhd", fullbox(1, 0, make([]byte, 8))), dinf(), stbl,
	}))
	mdia := box("mdia", concat([][]byte{
		mdhd(spec.Timescale, durationUnits), hdlr("vide"), minf,
	}))
	head := tkhd(1, spec.Timescale, durationUnits, spec.Width, spec.Height, spec.Rotation)
	if spec.StartNanos > 0 {
		startUnits := uint64(float64(spec.StartNanos) * float64(spec.Timescale) / 1e9)
		edts := box("edts", elstBox(startUnits, durationUnits-startUnits))
		return box("trak", concat([][]byte{head, edts, mdia}))
	}
	return box("trak", concat([][]byte{head, mdia}))
}

func buildAudioTrak(audio AudioContent, groups int, sizes, off []uint32) []byte {
	const group = 1024
	durationUnits := uint64(groups * group)
	deltas := make([]uint32, groups)
	for i := range deltas {
		deltas[i] = group
	}
	stbl := box("stbl", concat([][]byte{
		stsd(audioEntry(audio.Rate)), sttsBox(deltas),
		stscBox(groups), stszBox(sizes), stcoBox(off),
	}))
	minf := box("minf", concat([][]byte{
		box("smhd", fullbox(0, 0, make([]byte, 4))), dinf(), stbl,
	}))
	mdia := box("mdia", concat([][]byte{
		mdhd(audio.Rate, durationUnits), hdlr("soun"), minf,
	}))
	head := tkhd(2, audio.Rate, durationUnits, 0, 0, 0)
	return box("trak", concat([][]byte{head, mdia}))
}
