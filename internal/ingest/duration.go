package ingest

import (
	"fmt"

	"pwf/internal/media"
)

// Codec identifiers used across ingest, quality and planner packages.
const (
	CodecOpus = "opus"
	CodecAAC  = "aac"
)

// opusFrameDurationsUs maps the 32 Opus TOC config values to the frame
// duration in microseconds. Table taken from RFC 6716 section 3.1:
// configs 0..11/16..19 use 10/20/40/60 ms, configs 12..15 are the WB/SWB
// 20/40/60 ms entries, and configs 20..31 are the 2.5 ms variants.
var opusFrameDurationsUs = [32]int64{
	10000, 20000, 40000, 60000, // 0..3   NB
	10000, 20000, 40000, 60000, // 4..7   MB
	10000, 20000, 40000, 60000, // 8..11  WB
	20000, 40000, 60000, // 12..14 SWB (20/40/60 ms); 15 filled below
	10000, 20000, 20000, 60000, // 15(patched),16..19 FB
	2500, 5000, // 20..21 NB 2.5ms
	2500, 5000, // 22..23 MB
	2500, 5000, // 24..25 WB
	2500, 5000, // 26..27 SWB
	2500, 5000, // 28..29 FB
	2500, 5000, // 30..31 unassigned
}

func init() {
	opusFrameDurationsUs[15] = 60000 // config 15: SWB 60 ms
}

// opusClock is the mandated RTP clock rate for Opus (RFC 7587).
const opusClock uint32 = 48000

// OpusPacketDuration returns the whole-tick duration of an Opus packet on
// the 48 kHz clock, computed solely from the TOC byte per RFC 6716. The
// returned value is exact because every legal Opus frame duration is a
// whole multiple of 1/48000 s.
func OpusPacketDuration(payload []byte) (media.Duration, error) {
	if len(payload) == 0 {
		return media.Duration{}, media.NewError(media.KindUndetermined, media.StageParseRTP,
			"empty Opus payload: TOC byte absent", nil)
	}
	toc := payload[0]
	config := int(toc >> 3)
	code := toc & 0x03
	frames, ok := opusFrameCount(code, payload)
	if !ok {
		return media.Duration{}, media.NewError(media.KindUndetermined, media.StageParseRTP,
			fmt.Sprintf("Opus code-3 packet lacks a valid frame count byte"), nil)
	}
	frameUs := opusFrameDurationsUs[config]
	totalUs := frameUs * int64(frames)
	// us -> 48 kHz ticks, exact: tick = 1e6/48000 us, so ticks = us*48000/1e6.
	ticks := totalUs * int64(opusClock) / 1_000_000
	tb, _ := media.NewTimeBase(opusClock)
	return media.NewDuration(ticks, tb)
}

// opusFrameCount resolves the number of frames in a packet from the TOC
// code: 0 = 1 frame, 1 = 2 equal frames, 2 = 2 unequal, 3 = arbitrary
// count encoded in the payload's first frame-count byte.
func opusFrameCount(code byte, payload []byte) (int, bool) {
	switch code {
	case 0:
		return 1, true
	case 1, 2:
		return 2, true
	default:
		if len(payload) < 2 {
			return 0, false
		}
		n := int(payload[1] & 0x3f)
		if n < 1 || n > 48 {
			return 0, false
		}
		return n, true
	}
}

// AACPacketDuration parses an RFC 3640 AAC-hbr RTP payload and returns the
// summed duration of its access units on clockRate. The AU-headers-length
// and each AU header are validated so an undetermined duration is an
// error rather than a guess.
func AACPacketDuration(payload []byte, clockRate uint32) (media.Duration, error) {
	tb, err := media.NewTimeBase(clockRate)
	if err != nil {
		return media.Duration{}, media.NewError(media.KindUndetermined, media.StageParseRTP,
			"AAC stream has no usable clock rate", err)
	}
	auCount, auDataLengths, err := parseAUHeaders(payload)
	if err != nil {
		return media.Duration{}, err
	}
	// Each AU is one AAC frame: duration = 1024 samples per frame.
	ticks, err := mulDivChecked(int64(auCount)*1024, 1, 1)
	if err != nil {
		return media.Duration{}, err
	}
	d, err := media.NewDuration(ticks, tb)
	if err != nil {
		return media.Duration{}, err
	}
	// Validate that every declared AU actually fits in the payload so a
	// truncated packet is reported at parse time. The AU data starts after
	// the 2-byte length field plus the AU-headers section.
	var dataTotal int
	for _, l := range auDataLengths {
		dataTotal += l
	}
	dataStart := 2 + int(auHeaderLen(payload)/8)
	if dataStart+dataTotal > len(payload) {
		return media.Duration{}, media.NewError(media.KindCorrupt, media.StageParseRTP,
			fmt.Sprintf("AAC AU data exceeds payload: need %d bytes, have %d",
				dataStart+dataTotal, len(payload)), nil)
	}
	return d, nil
}

// parseAUHeaders returns the AU count and per-AU data lengths declared by
// the 16-bit AU-headers section.
func parseAUHeaders(payload []byte) (int, []int, error) {
	if len(payload) < 2 {
		return 0, nil, media.NewError(media.KindUndetermined, media.StageParseRTP,
			"AAC payload too short for AU-headers-length", nil)
	}
	headerBits := uint16(payload[0])<<8 | uint16(payload[1])
	if headerBits == 0 || headerBits%16 != 0 {
		return 0, nil, media.NewError(media.KindUndetermined, media.StageParseRTP,
			fmt.Sprintf("invalid AU-headers-length %d bits", headerBits), nil)
	}
	headerBytes := int(headerBits / 8)
	if 2+headerBytes > len(payload) {
		return 0, nil, media.NewError(media.KindCorrupt, media.StageParseRTP,
			"AU-headers section exceeds payload", nil)
	}
	count := headerBytes / 2
	lengths := make([]int, 0, count)
	for i := 0; i < count; i++ {
		// AU header: 13-bit AU size, 3-bit AU index.
		h := uint16(payload[2+2*i])<<8 | uint16(payload[3+2*i])
		size := int(h >> 3)
		if size == 0 {
			return 0, nil, media.NewError(media.KindUndetermined, media.StageParseRTP,
				"AAC AU header declares zero-size access unit", nil)
		}
		lengths = append(lengths, size)
	}
	return count, lengths, nil
}

func auHeaderLen(payload []byte) uint16 {
	return uint16(payload[0])<<8 | uint16(payload[1])
}

// mulDivChecked guards against integer overflow in tick arithmetic.
func mulDivChecked(a, b, c int64) (int64, error) {
	if c == 0 {
		return 0, fmt.Errorf("divide by zero")
	}
	if a != 0 && b > (1<<63-1)/a {
		return 0, fmt.Errorf("tick arithmetic overflow: %d*%d", a, b)
	}
	return a * b / c, nil
}
