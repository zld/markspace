// Package timeline (rtp part) validates that RTP timestamps shared by the
// same media source unwrap consistently onto the unified microsecond base.
package timeline

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"

	"mediaroute/internal/model"
)

// RTPClockRate is the RTP clock used by the generated fixtures (90 kHz video).
const RTPClockRate uint32 = 90000

// RTPFrame is one RTP packet reduced to the fields the mapper needs.
type RTPFrame struct {
	SequenceNumber uint16
	Timestamp      uint32
	Marker         bool
}

// UnwrapRTP expands 32-bit RTP timestamps modulo 2^32 into a 64-bit stream.
// Each step uses the signed 32-bit difference, so a wrap in either direction
// is handled without special cases. The returned count is the number of times
// the modulo space was crossed upward.
func UnwrapRTP(timestamps []uint32) ([]uint64, int) {
	out := make([]uint64, len(timestamps))
	if len(timestamps) == 0 {
		return out, 0
	}
	base := uint64(timestamps[0])
	var extended uint64
	wraps := 0
	lastHigh := base >> 32
	out[0] = base
	prev := timestamps[0]
	for i := 1; i < len(timestamps); i++ {
		delta := int64(int32(timestamps[i] - prev))
		if delta >= 0 {
			extended += uint64(delta)
		} else {
			back := uint64(-delta)
			if back > extended {
				back = extended
			}
			extended -= back
		}
		out[i] = base + extended
		if high := out[i] >> 32; high > lastHigh {
			wraps += int(high - lastHigh)
			lastHigh = high
		}
		prev = timestamps[i]
	}
	return out, wraps
}

// ParseRTPPackets decodes raw RTP datagrams. Invalid packets are reported as
// an error carrying the packet index so the caller can locate the damage.
func ParseRTPPackets(datagrams [][]byte) ([]RTPFrame, error) {
	frames := make([]RTPFrame, 0, len(datagrams))
	for i, dg := range datagrams {
		var pkt rtp.Packet
		if err := pkt.Unmarshal(dg); err != nil {
			return nil, model.WrapTaskError(model.CategoryRTPSync, Stage,
				fmt.Sprintf("rtp packet %d is malformed", i), err)
		}
		frames = append(frames, RTPFrame{
			SequenceNumber: pkt.SequenceNumber,
			Timestamp:      pkt.Timestamp,
			Marker:         pkt.Marker,
		})
	}
	return frames, nil
}

// ValidateRTP unwraps the supplied timestamps, maps them onto microseconds at
// the given clock rate and compares each mapped instant with the matching
// expected media time. expected may be nil, in which case only monotonicity
// after unwrapping is checked. A skew above tolerance marks Matched false.
func ValidateRTP(frames []RTPFrame, clockRate uint32, expected []model.Micros, tolerance model.Micros) (*model.RTPReport, error) {
	if clockRate == 0 {
		return nil, model.NewTaskError(model.CategoryZeroTimescale, Stage, "rtp clock rate is zero")
	}
	if len(expected) != 0 && len(expected) != len(frames) {
		return nil, model.NewTaskError(model.CategoryRTPSync, Stage,
			fmt.Sprintf("expected timeline has %d entries for %d packets", len(expected), len(frames)))
	}
	ts := make([]uint32, len(frames))
	for i := range frames {
		ts[i] = frames[i].Timestamp
	}
	unwrapped, wraps := UnwrapRTP(ts)
	report := &model.RTPReport{Packets: len(frames), Wraps: wraps, ClockRate: clockRate, Matched: true}
	// RTP timestamps carry a relative media clock, so the unified timeline is
	// anchored at the first packet. Subtracting the first unwrapped value
	// keeps the mapping independent of where in the 32-bit space we started.
	if len(unwrapped) > 0 {
		first := unwrapped[0]
		for i := range unwrapped {
			unwrapped[i] -= first
		}
	}
	var maxSkew model.Micros
	for i, u := range unwrapped {
		if i > 0 && u < unwrapped[i-1] {
			return nil, model.NewTaskError(model.CategoryRTPSync, Stage,
				fmt.Sprintf("rtp timestamp unwrap regressed at packet %d", i))
		}
		micros, err := model.TicksToMicros(u, clockRate)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			report.FirstMicros = micros
		}
		report.LastMicros = micros
		if len(expected) != 0 {
			skew := model.AbsMicros(micros, expected[i])
			if skew > maxSkew {
				maxSkew = skew
			}
		}
	}
	report.MaxSkewMicros = maxSkew
	if tolerance > 0 && maxSkew > tolerance {
		report.Matched = false
	}
	return report, nil
}

// EncodeRTPHeader builds a minimal valid RTP packet header for fixtures and
// tests. Payload is intentionally empty.
func EncodeRTPHeader(seq uint16, timestamp uint32, marker bool) []byte {
	buf := make([]byte, 12)
	buf[0] = 0x80 // version 2, no padding, no extension, no CSRC
	if marker {
		buf[1] |= 0x80
	}
	buf[1] |= 96 // dynamic payload type
	binary.BigEndian.PutUint16(buf[2:4], seq)
	binary.BigEndian.PutUint32(buf[4:8], timestamp)
	binary.BigEndian.PutUint32(buf[8:12], 0xCAFE)
	return buf
}
