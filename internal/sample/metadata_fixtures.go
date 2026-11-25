package sample

import "mediacheck/internal/ebml"

func buildHeader() []byte {
	payload := []byte{}
	payload = appendUint(payload, ebml.IDVersion, 1)
	payload = appendUint(payload, ebml.IDReadVersion, 1)
	payload = appendUint(payload, ebml.IDMaxIDWidth, 4)
	payload = appendUint(payload, ebml.IDMaxSizeWidth, 8)
	payload = appendString(payload, ebml.IDDocType, "webm")
	return element(ebml.IDEBMLHeader, payload)
}

func buildInfo() []byte {
	payload := []byte{}
	payload = appendUint(payload, ebml.IDTimecodeScale, 1_000_000)
	payload = appendString(payload, ebml.IDMuxingApp, "mediacheck-sample")
	payload = appendString(payload, ebml.IDWritingApp, "mediacheck")
	return element(ebml.IDInfo, payload)
}

func buildNormalTracks() []byte {
	videoColour := []byte{}
	videoColour = appendUint(videoColour, ebml.IDMatrixCoeffs, 1)
	videoColour = appendUint(videoColour, ebml.IDTransferChars, 1)
	videoColour = appendUint(videoColour, ebml.IDColourPrimaries, 1)

	video := []byte{}
	video = appendUint(video, ebml.IDPixelWidth, 640)
	video = appendUint(video, ebml.IDPixelHeight, 360)
	video = appendElement(video, ebml.IDColour, videoColour)

	videoEntry := []byte{}
	videoEntry = appendUint(videoEntry, ebml.IDTrackNumber, 1)
	videoEntry = appendUint(videoEntry, ebml.IDTrackUID, 101)
	videoEntry = appendUint(videoEntry, ebml.IDTrackType, ebml.TrackTypeVideo)
	videoEntry = appendString(videoEntry, ebml.IDCodecID, "V_VP9")
	videoEntry = appendElement(videoEntry, ebml.IDVideo, video)

	audio := []byte{}
	audio = appendUint(audio, ebml.IDChannels, 2)
	audio = appendUint(audio, ebml.IDChannelLayout, 2) // stereo
	audio = appendFloat(audio, ebml.IDSamplingFreq, 48_000)
	audio = appendUint(audio, ebml.IDBitDepth, 32)

	audioEntry := []byte{}
	audioEntry = appendUint(audioEntry, ebml.IDTrackNumber, 2)
	audioEntry = appendUint(audioEntry, ebml.IDTrackUID, 202)
	audioEntry = appendUint(audioEntry, ebml.IDTrackType, ebml.TrackTypeAudio)
	audioEntry = appendString(audioEntry, ebml.IDCodecID, "A_OPUS")
	audioEntry = appendElement(audioEntry, ebml.IDAudio, audio)

	tracksPayload := append(element(ebml.IDTrackEntry, videoEntry), element(ebml.IDTrackEntry, audioEntry)...)
	return element(ebml.IDTracks, tracksPayload)
}

func buildConflictTracks() []byte {
	audio1 := []byte{}
	audio1 = appendUint(audio1, ebml.IDChannels, 2)
	audio1 = appendUint(audio1, ebml.IDChannelLayout, 2)
	audio1 = appendFloat(audio1, ebml.IDSamplingFreq, 48_000)

	entry1 := []byte{}
	entry1 = appendUint(entry1, ebml.IDTrackNumber, 1)
	entry1 = appendUint(entry1, ebml.IDTrackUID, 11)
	entry1 = appendUint(entry1, ebml.IDTrackType, ebml.TrackTypeAudio)
	entry1 = appendString(entry1, ebml.IDCodecID, "A_OPUS")
	entry1 = appendElement(entry1, ebml.IDAudio, audio1)

	audio2 := []byte{}
	audio2 = appendUint(audio2, ebml.IDChannels, 2)
	audio2 = appendUint(audio2, ebml.IDChannelLayout, 6) // 5.1 requires six channels
	audio2 = appendFloat(audio2, ebml.IDSamplingFreq, 44_100)

	entry2 := []byte{}
	entry2 = appendUint(entry2, ebml.IDTrackNumber, 2)
	entry2 = appendUint(entry2, ebml.IDTrackUID, 22)
	entry2 = appendUint(entry2, ebml.IDTrackType, ebml.TrackTypeAudio)
	entry2 = appendString(entry2, ebml.IDCodecID, "A_OPUS")
	entry2 = appendUint(entry2, ebml.IDTrackDependsOn, 1)
	entry2 = appendElement(entry2, ebml.IDAudio, audio2)

	payload := append(element(ebml.IDTrackEntry, entry1), element(ebml.IDTrackEntry, entry2)...)
	return element(ebml.IDTracks, payload)
}
