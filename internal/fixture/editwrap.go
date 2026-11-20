package fixture

// edit-wrap profile: classic MP4, two tracks, edit list with an empty
// edit, B frames (positive ctts on video, negative ctts on audio).
//
// Video: timescale 90000, 12 samples, decode DTS step 10000.
// Presentation PTS (media ticks) chosen so that after the edit list
// (empty edit 9000, then media edit from media_time 81000) the movie
// presentation times land on exact milliseconds.

const (
	editMovieTimescale = 90000
	editVideoSamples   = 12
	editVideoDuration  = 10000 // decode step per sample
	editAudioSamples   = 10
	editAudioDuration  = 1024
	editAudioTimescale = 48000
)

var editVideoPTS = []int64{
	81000, 90000, 94500, 92250, 96750, 99000,
	103500, 101250, 108000, 105750, 112500, 110250,
}

// editVideoTypes: I=IDR for 0,1,6; P/B otherwise.
var editVideoTypes = []byte{'I', 'I', 'P', 'B', 'P', 'B', 'I', 'B', 'P', 'B', 'P', 'B'}

// videoSample builds one length-prefixed AVC sample with deterministic
// filler. kind: 'I' (IDR), 'P', 'B'.
func videoSample(idx int, kind byte) []byte {
	var nalHeader, sliceByte byte
	switch kind {
	case 'I':
		nalHeader, sliceByte = 0x65, 0xB0 // IDR, I slice
	case 'P':
		nalHeader, sliceByte = 0x61, 0xC0
	default: // 'B'
		nalHeader, sliceByte = 0x61, 0xA0
	}
	nal := append([]byte{nalHeader, sliceByte}, fill(uint32(idx+1), 100+(idx*37)%160)...)
	var out bw
	out.u32(uint32(len(nal)))
	out.bytes(nal)
	return out.buf
}

func audioSample(idx int) []byte {
	return fill(uint32(1000+idx), 96)
}

func elstBox() []byte {
	var p bw
	p.u32(2)     // entry count
	p.u32(9000)  // empty edit: 100ms of movie time
	p.i32(-1)    // media_time = -1
	p.u16(0)     // rate integer
	p.u16(0)     // rate fraction
	p.u32(45000) // media edit: 500ms of movie time
	p.i32(81000) // starting at media time 81000
	p.u16(1)     // rate 1.0
	p.u16(0)
	return fullBoxOf("elst", 0, 0, p.buf)
}

func videoStbl(chunkOffset uint32, sizes []uint32) []byte {
	var ctts bw
	ctts.u32(uint32(len(editVideoPTS)))
	for i, pts := range editVideoPTS {
		ctts.u32(1)
		ctts.i32(int32(pts - int64(i)*editVideoDuration))
	}
	var stss bw
	stss.u32(3)
	stss.u32(1)
	stss.u32(2)
	stss.u32(7)
	var stbl bw
	stbl.bytes(stsdAvc1(1280, 720))
	stbl.bytes(sttsFrom([][2]uint32{{editVideoSamples, editVideoDuration}}))
	stbl.bytes(fullBoxOf("ctts", 1, 0, ctts.buf)) // version 1: signed
	stbl.bytes(fullBoxOf("stss", 0, 0, stss.buf))
	stbl.bytes(stscFrom(1, editVideoSamples))
	stbl.bytes(stszFrom(sizes))
	stbl.bytes(stcoFrom([]uint32{chunkOffset}))
	return boxOf("stbl", stbl.buf)
}

func audioStbl(chunkOffset uint32, sizes []uint32) []byte {
	var ctts bw
	ctts.u32(1)
	ctts.u32(editAudioSamples)
	ctts.i32(-editAudioDuration) // negative composition offset
	var stbl bw
	stbl.bytes(stsdMp4a(editAudioTimescale))
	stbl.bytes(sttsFrom([][2]uint32{{editAudioSamples, editAudioDuration}}))
	stbl.bytes(fullBoxOf("ctts", 1, 0, ctts.buf))
	stbl.bytes(stscFrom(1, editAudioSamples))
	stbl.bytes(stszFrom(sizes))
	stbl.bytes(stcoFrom([]uint32{chunkOffset}))
	return boxOf("stbl", stbl.buf)
}

func trakVideo(chunkOffset uint32, sizes []uint32) []byte {
	var mdia bw
	mdia.bytes(mdhd(editMovieTimescale, editVideoSamples*editVideoDuration))
	mdia.bytes(hdlr("vide"))
	var minf bw
	minf.bytes(vmhd())
	minf.bytes(dinf())
	minf.bytes(videoStbl(chunkOffset, sizes))
	mdia.bytes(boxOf("minf", minf.buf))
	var trak bw
	trak.bytes(tkhd(1, 54000, 1280, 720, false))
	trak.bytes(boxOf("edts", elstBox()))
	trak.bytes(boxOf("mdia", mdia.buf))
	return boxOf("trak", trak.buf)
}

func trakAudio(chunkOffset uint32, sizes []uint32) []byte {
	var mdia bw
	mdia.bytes(mdhd(editAudioTimescale, editAudioSamples*editAudioDuration))
	mdia.bytes(hdlr("soun"))
	var minf bw
	minf.bytes(smhd())
	minf.bytes(dinf())
	minf.bytes(audioStbl(chunkOffset, sizes))
	mdia.bytes(boxOf("minf", minf.buf))
	var trak bw
	trak.bytes(tkhd(2, 19200, 0, 0, true))
	trak.bytes(boxOf("mdia", mdia.buf))
	return boxOf("trak", trak.buf)
}

func ftyp() []byte {
	var p bw
	p.str("isom")
	p.u32(1)
	p.str("isom")
	p.str("iso6")
	p.str("avc1")
	p.str("mp41")
	return boxOf("ftyp", p.buf)
}

func buildEditWrap() []byte {
	head := ftyp()

	var videoData, audioData bw
	var videoSizes, audioSizes []uint32
	for i := 0; i < editVideoSamples; i++ {
		s := videoSample(i, editVideoTypes[i])
		videoSizes = append(videoSizes, uint32(len(s)))
		videoData.bytes(s)
	}
	for i := 0; i < editAudioSamples; i++ {
		s := audioSample(i)
		audioSizes = append(audioSizes, uint32(len(s)))
		audioData.bytes(s)
	}
	mdatHeader := 8
	videoChunkOff := uint32(len(head) + mdatHeader)
	audioChunkOff := videoChunkOff + uint32(len(videoData.buf))

	var mdatPayload bw
	mdatPayload.bytes(videoData.buf)
	mdatPayload.bytes(audioData.buf)
	mdat := boxOf("mdat", mdatPayload.buf)

	var moov bw
	moov.bytes(mvhd(editMovieTimescale, 54000, 3))
	moov.bytes(trakVideo(videoChunkOff, videoSizes))
	moov.bytes(trakAudio(audioChunkOff, audioSizes))

	var out bw
	out.bytes(head)
	out.bytes(mdat)
	out.bytes(boxOf("moov", moov.buf))
	return out.buf
}
