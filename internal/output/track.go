package output

import (
	"medcheck/internal/clock"
)

func trakBox(
	trackID uint32,
	kind clock.StreamID,
	timescale uint32,
	samples []Sample,
	sizes []uint32,
	sampleEntry []byte,
	chunkOffset uint32,
	durationMS uint32,
	doc Document,
) ([]byte, error) {
	deltas := make([]uint32, len(samples))
	var durationUnits uint32
	for i, s := range samples {
		delta := uint32(0)
		if s.DurationNS > 0 {
			delta = uint32(s.DurationNS * int64(timescale) / 1_000_000_000)
		}
		if delta == 0 {
			delta = 1
		}
		deltas[i] = delta
		durationUnits += delta
	}

	sttsPayload := u32(uint32(len(deltas)))
	for _, d := range deltas {
		// Each entry has count=1 so variable durations stay exact.
		sttsPayload = append(sttsPayload, u32(1)...)
		sttsPayload = append(sttsPayload, u32(d)...)
	}
	stts := fullBox("stts", 0, 0, sttsPayload)

	stsc := fullBox("stsc", 0, 0, appendBytes(
		u32(1), // entry count
		u32(1), u32(uint32(len(samples))), u32(1),
	))

	stszPayload := appendBytes(u32(0), u32(uint32(len(samples))))
	for _, size := range sizes {
		stszPayload = append(stszPayload, u32(size)...)
	}
	stsz := fullBox("stsz", 0, 0, stszPayload)

	stco := fullBox("stco", 0, 0, appendBytes(u32(1), u32(chunkOffset)))
	stsd := fullBox("stsd", 0, 0, appendBytes(u32(1), sampleEntry))
	stbl := box("stbl", appendBytes(stsd, stts, stsc, stsz, stco))

	var mediaHeader, handler, minf []byte
	if kind == clock.StreamAudio {
		mediaHeader = mdhdBox(timescale, durationUnits)
		handler = hdlrBox("soun", "Medcheck Audio")
		minf = box("minf", appendBytes(audioMediaHeaderBox(), dinfBox(), stbl))
		tkhd := tkhdBox(trackID, durationMS, true, 0, 0)
		mdia := box("mdia", appendBytes(mediaHeader, handler, minf))
		return box("trak", appendBytes(tkhd, mdia)), nil
	}

	mediaHeader = mdhdBox(timescale, durationUnits)
	handler = hdlrBox("vide", "Medcheck Video")
	minf = box("minf", appendBytes(videoMediaHeaderBox(), dinfBox(), stbl))
	width := uint32(doc.Video.Width)
	height := uint32(doc.Video.Height)
	if width == 0 {
		width = 640
	}
	if height == 0 {
		height = 360
	}
	tkhd := tkhdBox(trackID, durationMS, false, width, height)
	mdia := box("mdia", appendBytes(mediaHeader, handler, minf))
	return box("trak", appendBytes(tkhd, mdia)), nil
}
