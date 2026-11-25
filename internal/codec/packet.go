// Package codec maps audio payloads onto a sample count on the shared
// nanosecond clock.
//
// Opus packets describe their own duration in the TOC byte; AAC frames in
// this project are carried as ADTS, whose header gives both the sample rate
// and the total frame length. When a payload does not support either parse
// the caller must supply the container-declared frame size, and the result
// is marked as inferred rather than parsed.
package codec

import (
	"example.com/assetcheck/internal/clock"
	"example.com/assetcheck/internal/fault"
)

// Kind identifies the audio packet format.
type Kind string

const (
	KindOpus Kind = "opus"
	KindAAC  Kind = "aac"
)

// Frame is the timing result for one packet.
type Frame struct {
	Kind       Kind
	SampleRate uint32
	Samples    uint64
	Duration   clock.Nanos
	Frames     int
	Inferred   bool
	Truncated  bool
}

// maxOpusPacketMs is the largest legal Opus packet duration (120 ms).
const maxOpusPacketMs = 120

// opusFrameSamples48k is the frame duration in 48 kHz samples indexed by
// TOC configuration (RFC 6716 section 3.1).
var opusFrameSamples48k = [32]uint64{
	480, 960, 1920, 2880, // SILK NB 10/20/40/60 ms
	480, 960, 1920, 2880, // SILK MB
	480, 960, 1920, 2880, // SILK WB
	480, 960, // Hybrid SWB 10/20
	480, 960, // Hybrid FB 10/20
	120, 240, 480, 960, // CELT NB 2.5/5/10/20
	120, 240, 480, 960, // CELT WB
	120, 240, 480, 960, // CELT SWB
	120, 240, 480, 960, // CELT FB
}

// AnalyzeOpus parses an Opus packet's TOC and returns its duration in
// 48 kHz samples. A packet that is shorter than its TOC frame layout
// requires is reported as truncated with the exact byte offset that is
// missing relative to the payload start (reported as 0 here; the scanner
// rebases it onto the input).
func AnalyzeOpus(payload []byte) (Frame, error) {
	if len(payload) == 0 {
		return Frame{}, fault.New(fault.StageCodec, "codec.opus.empty", fault.NoOffset, clock.Invalid, "opus packet has no TOC byte")
	}
	toc := payload[0]
	config := int(toc >> 3)
	frameSamples := opusFrameSamples48k[config]
	frameCount, need := opusFrameCount(toc, payload)
	if need > len(payload) {
		f := Frame{Kind: KindOpus, SampleRate: 48000, Samples: 0, Truncated: true}
		f.Duration = 0
		return f, fault.New(fault.StageCodec, "codec.opus.truncated", fault.NoOffset, clock.Invalid,
			"opus packet is shorter than its TOC frame layout (need "+itoa(uint64(need))+" bytes, have "+itoa(uint64(len(payload)))+")")
	}
	if frameCount <= 0 || frameCount > 48 {
		return Frame{}, fault.New(fault.StageCodec, "codec.opus.bad_frame_count", fault.NoOffset, clock.Invalid,
			"opus TOC declares an illegal frame count "+itoa(uint64(frameCount)))
	}
	samples := frameSamples * uint64(frameCount)
	ms := samples * 1000 / 48000
	if ms == 0 || ms > maxOpusPacketMs {
		return Frame{}, fault.New(fault.StageCodec, "codec.opus.duration_abnormal", fault.NoOffset, clock.Invalid,
			"opus packet duration "+itoa(ms)+"ms is outside 1.."+itoa(maxOpusPacketMs)+"ms")
	}
	dur, err := clock.FromSamples(samples, 48000)
	if err != nil {
		return Frame{}, fault.Wrap(fault.StageCodec, "codec.opus.duration_overflow", fault.NoOffset, clock.Invalid,
			"opus duration does not fit the time base", err)
	}
	return Frame{Kind: KindOpus, SampleRate: 48000, Samples: samples, Duration: dur, Frames: frameCount}, nil
}

// opusFrameCount returns the number of frames and the minimum number of
// payload bytes needed to describe them.
func opusFrameCount(toc byte, payload []byte) (frames, need int) {
	code := toc & 0x03
	switch code {
	case 0:
		return 1, 1
	case 1, 2:
		return 2, 2
	default: // code 3: frame count in the next byte's low 6 bits
		if len(payload) < 2 {
			return 0, 2
		}
		return int(payload[1] & 0x3F), 2
	}
}

// AnalyzeAAC parses an ADTS frame and returns its duration in samples at the
// stream's sample rate. If the payload is not ADTS (raw AAC inside MP4), it
// falls back to frameSize samples and marks the result inferred.
func AnalyzeAAC(payload []byte, sampleRate uint32, frameSize uint64) (Frame, error) {
	if len(payload) == 0 {
		return Frame{}, fault.New(fault.StageCodec, "codec.aac.empty", fault.NoOffset, clock.Invalid, "aac frame has no payload")
	}
	if len(payload) < 7 || payload[0] != 0xFF || payload[1]&0xF0 != 0xF0 {
		return analyzeRawAAC(payload, sampleRate, frameSize)
	}
	// ADTS header: sync(12) ID(1) layer(2) protection_absent(1)
	protectionAbsent := payload[1] & 0x01
	rateIndex := int(payload[2] >> 2 & 0x0F)
	rate, ok := adtsSampleRates[rateIndex]
	if !ok {
		return Frame{}, fault.New(fault.StageCodec, "codec.aac.bad_adts", fault.NoOffset, clock.Invalid,
			"adts sampling frequency index "+itoa(uint64(rateIndex))+" is reserved")
	}
	header := 7
	if protectionAbsent == 0 {
		header = 9
	}
	frameLen := int(payload[3]&0x03)<<11 | int(payload[4])<<3 | int(payload[5]>>5)
	if frameLen < header {
		return Frame{}, fault.New(fault.StageCodec, "codec.aac.bad_adts", fault.NoOffset, clock.Invalid,
			"adts frame length "+itoa(uint64(frameLen))+" is shorter than its header")
	}
	if frameLen > len(payload) {
		f := Frame{Kind: KindAAC, SampleRate: rate, Truncated: true}
		return f, fault.New(fault.StageCodec, "codec.aac.truncated", fault.NoOffset, clock.Invalid,
			"adts frame declares "+itoa(uint64(frameLen))+" bytes but only "+itoa(uint64(len(payload)))+" are present")
	}
	const samplesPerADTSFrame = 1024
	dur, err := clock.FromSamples(samplesPerADTSFrame, rate)
	if err != nil {
		return Frame{}, fault.Wrap(fault.StageCodec, "codec.aac.duration_overflow", fault.NoOffset, clock.Invalid,
			"aac duration does not fit the time base", err)
	}
	return Frame{Kind: KindAAC, SampleRate: rate, Samples: samplesPerADTSFrame, Duration: dur, Frames: 1, Inferred: false}, nil
}

func analyzeRawAAC(payload []byte, sampleRate uint32, frameSize uint64) (Frame, error) {
	if sampleRate == 0 {
		return Frame{}, fault.New(fault.StageCodec, "codec.aac.no_rate", fault.NoOffset, clock.Invalid,
			"raw aac payload has no ADTS header and no container sample rate")
	}
	if frameSize == 0 {
		frameSize = 1024
	}
	dur, err := clock.FromSamples(frameSize, sampleRate)
	if err != nil {
		return Frame{}, fault.Wrap(fault.StageCodec, "codec.aac.duration_overflow", fault.NoOffset, clock.Invalid,
			"aac duration does not fit the time base", err)
	}
	return Frame{Kind: KindAAC, SampleRate: sampleRate, Samples: frameSize, Duration: dur, Frames: 1, Inferred: true}, nil
}

var adtsSampleRates = map[int]uint32{
	0: 96000, 1: 88200, 2: 64000, 3: 48000, 4: 44100, 5: 32000,
	6: 24000, 7: 22050, 8: 16000, 9: 12000, 10: 11025, 11: 8000, 12: 7350,
}

// Timestamp is one RTP media-clock reading paired with its delivery order.
type Timestamp struct {
	Seq  uint16
	Raw  uint32
	Ext  uint64
	Time clock.Nanos
}

// TimestampTracker unwraps 32-bit RTP timestamps and flags regressions.
//
// Timestamps advance by the packet's sample count, so a reading below the
// previous extended reading by more than one packet's worth of samples is
// not a wrap: it is a non-monotonic stream and must be reported, not
// silently unwrapped.
type TimestampTracker struct {
	started bool
	lastExt uint64
	maxStep uint64
}

// NewTimestampTracker returns a tracker that tolerates steps up to maxStep
// samples in either direction (a zero maxStep disables the tolerance check
// and only reports strict regressions).
func NewTimestampTracker(maxStep uint64) *TimestampTracker {
	return &TimestampTracker{maxStep: maxStep}
}

// Accept extends ts and returns the extended value. The first result is the
// media-clock position; the second reports whether the reading is monotonic.
//
// A reading that is lower than the previous value is either a wrap of the
// 32-bit media clock or a genuine regression. It is treated as a wrap (and
// shifted into the next cycle) only when the forward distance implied by
// that wrap is within maxStep; otherwise the reading is reported as
// non-monotonic and does not move the clock.
func (t *TimestampTracker) Accept(ts uint32) (uint64, bool) {
	if !t.started {
		t.started = true
		t.lastExt = uint64(ts)
		return t.lastExt, true
	}
	base := t.lastExt &^ 0xFFFFFFFF
	candidate := base | uint64(ts)
	if candidate < t.lastExt {
		wrapped := candidate + 0x100000000
		if wrapped-t.lastExt <= t.maxStep {
			candidate = wrapped
		} else {
			return t.lastExt, false
		}
	}
	t.lastExt = candidate
	return candidate, true
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
