package medpipe

// keyPayload and pPayload build codec-valid-looking frame payloads of size n.
func keyPayload(codec Codec, n int) []byte {
	b := make([]byte, n)
	switch codec {
	case CodecH264:
		b[0] = 0x65 // nal_ref_idc=3, IDR slice
	case CodecH265:
		b[0], b[1] = (h265NALIDR_W_RADL<<1)|0, 0x01
	case CodecAV1:
		b[0] = 0x30 // OBU frame header, forbidden=0 type=6
		b[1] = 0x00 // show_existing=0, frame_type=0 => key
	}
	return b
}

func pPayload(codec Codec, n int) []byte {
	b := make([]byte, n)
	switch codec {
	case CodecH264:
		b[0] = 0x41 // non-IDR slice
	case CodecH265:
		b[0], b[1] = (h265NALTRAIL_N<<1)|0, 0x01
	case CodecAV1:
		b[0] = 0x30
		b[1] = 0x02 // frame_type=1 => inter
	}
	return b
}

// buildNormalVideo emits 30 fps frames, keyframes every 12th frame, with a
// quality marker on each keyframe.
func buildNormalVideo(codec Codec, p FixtureParams) *CaptureBuilder {
	cb := NewCaptureBuilder()
	var seq uint16
	for i := 0; i < p.VideoFrames; i++ {
		ts := uint32(i * int(fixtureVideoDelta))
		if i%12 == 0 {
			putMetaPacket(cb, codec, &seq, ts, FrameMeta{Quality: 85 + uint8(i%8)})
		}
		var frame []byte
		if i%12 == 0 {
			frame = keyPayload(codec, p.VideoBytes)
		} else {
			frame = pPayload(codec, p.VideoBytes)
		}
		cb.Packet(seq, ts, true, frame)
		seq++
	}
	return cb
}

// buildAudio emits 48 kHz, 20 ms frames with an explicit stereo header byte.
func buildAudio(p FixtureParams) *CaptureBuilder {
	cb := NewCaptureBuilder()
	var seq uint16
	for i := 0; i < p.AudioFrames; i++ {
		ts := uint32(i * int(fixtureAudioDelta))
		payload := make([]byte, p.AudioBytes)
		payload[0] = 0xC0 // channels=2 marker, consumed as opaque data
		cb.Packet(seq, ts, true, payload)
		seq++
	}
	return cb
}

// buildTruncated cuts the final record mid-way.
func buildTruncated(p FixtureParams) *CaptureBuilder {
	return buildNormalVideo(CodecH264, p).Truncate(7)
}

// buildRollback moves one timestamp backwards; the clock repairs it via delta.
func buildRollback(p FixtureParams) *CaptureBuilder {
	cb := NewCaptureBuilder()
	var seq uint16
	const glitchAt = 24
	for i := 0; i < p.VideoFrames; i++ {
		ts := uint32(i * int(fixtureVideoDelta))
		if i == glitchAt {
			ts -= 4500 // negative delta: this sample lands 1500 ticks behind
		}
		var frame []byte
		if i%12 == 0 {
			frame = keyPayload(CodecH265, p.DegradedBytes)
		} else {
			frame = pPayload(CodecH265, p.DegradedBytes)
		}
		cb.Packet(seq, ts, true, frame)
		seq++
	}
	return cb
}

// buildConflict tags every frame with color metadata; the second half
// switches triples, producing counted conflicts with first-known adopted.
func buildConflict(p FixtureParams) *CaptureBuilder {
	cb := NewCaptureBuilder()
	var seq uint16
	for i := 0; i < p.VideoFrames; i++ {
		ts := uint32(i * int(fixtureVideoDelta))
		meta := FrameMeta{
			ColorPrimaries: PrimariesBT709, Transfer: TransferSRGB, Matrix: MatrixBT709, Quality: 90,
		}
		if i >= p.VideoFrames/2 {
			meta.ColorPrimaries, meta.Transfer, meta.Matrix = PrimariesBT2020, TransferPQ, MatrixBT2020NCL
		}
		putMetaPacket(cb, CodecAV1, &seq, ts, meta)
		var frame []byte
		if i%12 == 0 {
			frame = keyPayload(CodecAV1, p.DegradedBytes)
		} else {
			frame = pPayload(CodecAV1, p.DegradedBytes)
		}
		cb.Packet(seq, ts, true, frame)
		seq++
	}
	return cb
}

// buildAmbiguous emits an unrepairable duplicate (Delta=0).
func buildAmbiguous(p FixtureParams) *CaptureBuilder {
	cb := NewCaptureBuilder()
	cb.Packet(0, 0, true, keyPayload(CodecH264, p.DegradedBytes))
	cb.Packet(1, 3000, true, pPayload(CodecH264, p.DegradedBytes))
	cb.Packet(2, 3000, true, pPayload(CodecH264, p.DegradedBytes)) // duplicate
	cb.Packet(3, 6000, true, pPayload(CodecH264, p.DegradedBytes))
	return cb
}

// putMetaPacket appends a metadata unit using the codec's NAL/OBU framing.
func putMetaPacket(cb *CaptureBuilder, codec Codec, seq *uint16, ts uint32, meta FrameMeta) {
	body := buildMetaPayload(meta)
	var unit []byte
	switch codec {
	case CodecH264:
		unit = append([]byte{h264NALSEI}, body...)
	case CodecH265:
		unit = append([]byte{h265NALPREFIX_SEI << 1, 0x01}, body...)
	default: // AV1 metadata OBU
		unit = append([]byte{metadataNALHeader(CodecAV1)}, body...)
	}
	cb.Packet(*seq, ts, true, unit)
	*seq++
}
