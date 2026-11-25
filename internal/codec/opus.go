// Package codec converts Opus and AAC packet payloads into sample counts on
// the shared integer tick clock. A packet whose duration cannot be decided
// is never guessed: it yields a structured finding with a code, the packet
// sequence and the affected time span.
package codec

import (
	"errors"

	"tracecheck/internal/model"
)

// ErrEmpty is returned when a packet carries no payload bytes at all.
var ErrEmpty = errors.New("empty codec packet")

// opusFrameSamples is the per-frame sample count at the fixed 48 kHz Opus
// reference clock, indexed by the 5-bit TOC config field (RFC 6716 table 1).
// 120=2.5ms, 240=5ms, 480=10ms, 960=20ms, 1920=40ms and 2880=60ms.
var opusFrameSamples = [32]int64{
	// SILK-only, grouped by frame duration (10/20/40/60 ms) across
	// NB, MB and WB modes per RFC 6716 table 1.
	480, 480, 480, // configs 0-2:   NB/MB/WB 10 ms
	960, 960, 960, // configs 3-5:   NB/MB/WB 20 ms
	1920, 1920, 1920, // configs 6-8:  NB/MB/WB 40 ms
	2880, 2880, 2880, // configs 9-11: NB/MB/WB 60 ms
	// Hybrid: SWB and FB, each 10/20 ms.
	480, 960, 480, 960,
	// CELT-only: NB, MB, WB, SWB, FB, each 2.5/5/10/20 ms.
	120, 240, 480, 960,
	120, 240, 480, 960,
	120, 240, 480, 960,
	120, 240, 480, 960,
}

// ErrTruncated is returned when a payload ends before its declared frames.
var ErrTruncated = errors.New("truncated codec packet")

// ErrUndecidable is returned when packet framing does not determine a
// duration (unknown code or an illegal code combination).
var ErrUndecidable = errors.New("undecidable packet duration")

// OpusResult is the parsed duration of one Opus packet.
type OpusResult struct {
	Frames     int   `json:"frames"`
	Samples    int64 `json:"samples"`
	FrameTicks model.Tick `json:"frame_ticks"`
}

// ParseOpus reads the TOC byte and code framing (RFC 6716 section 3.2) and
// returns the total sample count at 48 kHz. It validates only framing and
// length: codec payload content is never decoded or repaired.
func ParseOpus(payload []byte) (OpusResult, error) {
	if len(payload) == 0 {
		return OpusResult{}, ErrEmpty
	}
	toc := payload[0]
	config := toc >> 3
	frameSamples := opusFrameSamples[config]
	code := toc & 0x03

	frames, minBytes, err := opusFrameCount(code, payload)
	if err != nil {
		return OpusResult{}, err
	}
	if len(payload) < minBytes {
		return OpusResult{}, ErrTruncated
	}
	ticks, err := model.TicksFromSamples(frameSamples, 48_000)
	if err != nil {
		return OpusResult{}, err
	}
	return OpusResult{
		Frames:     frames,
		Samples:    frameSamples * int64(frames),
		FrameTicks: ticks,
	}, nil
}

// opusFrameCount resolves the number of frames and the minimum payload
// length for the TOC code. VBR and CBR code 1 packets need at least one
// length byte per frame; code 2/3 packets use no per-frame lengths.
func opusFrameCount(code byte, payload []byte) (frames, minBytes int, err error) {
	switch code {
	case 0:
		// One frame.
		return 1, 1, nil
	case 1:
		// Two frames, one TOC plus at least one length byte and data.
		return 2, 3, nil
	case 2:
		// Two frames, no length fields.
		return 2, 2, nil
	case 3:
		// Arbitrary number of frames described by one padding byte.
		if len(payload) < 2 {
			return 0, 0, ErrTruncated
		}
		b := payload[1]
		frames = int(b & 0x3f)
		if frames == 0 {
			return 0, 0, ErrUndecidable
		}
		paddingBytes := int(b >> 6)
		// Each set padding bit adds a byte; the last set bit stops the run.
		if paddingBytes > 0 {
			if len(payload) < 2+paddingBytes {
				return 0, 0, ErrTruncated
			}
			for i := 0; i < paddingBytes; i++ {
				pb := payload[2+i]
				paddingBytes += int(pb >> 7)
				if len(payload) < 2+paddingBytes {
					return 0, 0, ErrTruncated
				}
			}
		}
		return frames, 2 + paddingBytes + frames, nil
	default:
		return 0, 0, ErrUndecidable
	}
}

// OpusDurationTicks is a convenience returning total packet duration ticks.
func OpusDurationTicks(payload []byte) (model.Tick, error) {
	res, err := ParseOpus(payload)
	if err != nil {
		return 0, err
	}
	ticks, err := model.TicksFromSamples(res.Samples, 48_000)
	if err != nil {
		return 0, err
	}
	return ticks, nil
}
