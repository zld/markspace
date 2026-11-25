package fixtures

// The normal fixture is a six-second replay: a 90 kHz video track with 180
// 33.33 ms frames in two-second GOPs, and a 48 kHz audio track with 288 frames.
// Cut events land on GOP boundaries so the derived clips tile exactly.

const (
	videoFrameDur = 3000 // 90 kHz ticks = 33.333 ms
	audioFrameDur = 1000 // 48 kHz ticks = 20.833 ms
	framesPerGOP  = 60
	totalVideo    = 180
	totalAudio    = 288
)

func mediaTrack(id uint32, handler, codec string, ts uint32, count int, dur uint32) trackSpec {
	samples := make([]sampleSpec, count)
	for i := range samples {
		size := uint32(2048)
		if handler == "soun" {
			size = 256
		}
		payload := make([]byte, size)
		for j := range payload {
			payload[j] = byte(int(id)*31 + i + j)
		}
		samples[i] = sampleSpec{size: size, dur: dur, payload: payload}
	}
	return trackSpec{id: id, handler: handler, timescale: ts, codec: codec, samples: samples}
}

func normalMedia() []byte {
	return buildMP4([]trackSpec{
		mediaTrack(1, "vide", "avc1", normalVideoTS, totalVideo, videoFrameDur),
		mediaTrack(2, "soun", "mp4a", normalAudioTS, totalAudio, audioFrameDur),
	}, 1000)
}

// timebaseMedia uses a 30 kHz video clock: 1001-tick frames are 33.3667 ms, so
// sample boundaries carry a 2/3 microsecond fraction that normalization must
// report rather than round.
func timebaseMedia() []byte {
	tr := mediaTrack(1, "vide", "avc1", timebaseTS, totalVideo, 1001)
	return buildMP4([]trackSpec{tr}, 1000)
}

// timebaseVideoEnd is the exact normalized end of timebaseMedia in
// microseconds: 180 * 1001 ticks at 30 kHz, floored.
const timebaseVideoEnd = totalVideo * 1001 * 1_000_000 / timebaseTS

// eventSpecs are expressed in microseconds; the helper converts them with the
// fixture clock rate so the relationship between wall time and RTP ticks is
// explicit rather than a magic constant.
func atUS(us uint64, rate uint32) uint32 {
	return uint32(us * uint64(rate) / 1_000_000)
}

func normalEvents() []eventSpec {
	const rate = wrapClockRate
	return []eventSpec{
		{ssrc: 0xA1, seq: 1, ts: atUS(0, rate), marker: true, payload: "open"},
		{ssrc: 0xA1, seq: 2, ts: atUS(2_000_000, rate), marker: true, payload: "cut-2s"},
		{ssrc: 0xA1, seq: 3, ts: atUS(4_000_000, rate), marker: true, payload: "cut-4s"},
		{ssrc: 0xA1, seq: 4, ts: atUS(6_000_000, rate), marker: true, payload: "close"},
	}
}

// wrapEvents crosses 2^32 exactly once between the second and third trigger.
func wrapEvents() []eventSpec {
	const rate = wrapClockRate
	base := uint32(0xFFFFFFFF) - atUS(2_500_000, rate)
	return []eventSpec{
		{ssrc: 0xB2, seq: 10, ts: base, marker: true, payload: "open"},
		{ssrc: 0xB2, seq: 11, ts: base + atUS(2_000_000, rate), marker: true, payload: "cut-2s"},
		{ssrc: 0xB2, seq: 12, ts: base + atUS(4_500_000, rate), marker: true, payload: "cut-4.5s"},
		{ssrc: 0xB2, seq: 13, ts: base + atUS(6_000_000, rate), marker: true, payload: "close"},
	}
}

func timebaseEvents() []eventSpec {
	const rate = wrapClockRate
	// The closing trigger lands exactly on the floored video end so the
	// timeline is closed without rounding it up past the last sample.
	return []eventSpec{
		{ssrc: 0xC3, seq: 20, ts: atUS(0, rate), marker: true, payload: "open"},
		{ssrc: 0xC3, seq: 21, ts: atUS(2_001_000, rate), marker: true, payload: "cut-odd"},
		{ssrc: 0xC3, seq: 22, ts: atUS(4_000_000, rate), marker: true, payload: "cut-4s"},
		{ssrc: 0xC3, seq: 23, ts: atUS(uint64(timebaseVideoEnd), rate), marker: true, payload: "close"},
	}
}

// duplicateEvents repeats one trigger byte-for-byte (same SSRC, sequence and
// timestamp) and adds a second trigger at an identical timestamp with a new
// sequence number.
func duplicateEvents() []eventSpec {
	const rate = wrapClockRate
	ev := normalEvents()
	dup := ev[1]
	out := []eventSpec{ev[0], dup, dup}
	out = append(out, ev[2:]...)
	return out
}
