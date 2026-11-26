package repair

import (
	"math"

	"github.com/pion/rtp"

	"mediarepair/internal/media"
	"mediarepair/internal/source"
)

type FrameTime struct {
	Sequence       int   `json:"sequence"`
	RawTicks       int64 `json:"raw_ticks"`
	RawTimeUS      int64 `json:"raw_time_us"`
	RepairedTimeUS int64 `json:"repaired_time_us"`
	DeviationUS    int64 `json:"deviation_us"`
	Corrected      bool  `json:"corrected"`
}

type Report struct {
	Frames         []FrameTime `json:"frames"`
	RepairedCount  int         `json:"repaired_count"`
	MaxDeviationUS int64       `json:"max_deviation_us"`
}

// RepairManifest generates monotonic sequence/frame-rate times. A raw tick
// value is counted as corrected when it differs from the generated time by
// more than toleranceUS.
func RepairManifest(m *source.Manifest, toleranceUS int64) *Report {
	if toleranceUS <= 0 {
		toleranceUS = 1000
	}
	rep := &Report{}
	for i, fr := range m.Frames {
		seqIndex := fr.Sequence - 1
		if seqIndex < 0 {
			seqIndex = i
		}
		rawUS := fr.Timebase.Microseconds(fr.RawTicks)
		fixedUS := media.ExpectedMicroseconds(seqIndex, m.FrameRate)
		dev := rawUS - fixedUS
		if dev < 0 {
			dev = -dev
		}
		corrected := dev > toleranceUS
		if corrected {
			rep.RepairedCount++
		}
		rep.MaxDeviationUS = max64(rep.MaxDeviationUS, dev)
		rep.Frames = append(rep.Frames, FrameTime{
			Sequence:       fr.Sequence,
			RawTicks:       fr.RawTicks,
			RawTimeUS:      rawUS,
			RepairedTimeUS: fixedUS,
			DeviationUS:    rawUS - fixedUS,
			Corrected:      corrected,
		})
	}
	return rep
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// ParseRTPPacket uses pion/rtp only for local byte parsing; there is no
// network session.
func ParseRTPPacket(b []byte) (timestamp uint32, marker bool, err error) {
	var p rtp.Packet
	if err := p.Unmarshal(b); err != nil {
		return 0, false, media.E(media.CodeFormat, "rtp.unmarshal", err)
	}
	return p.Timestamp, p.Marker, nil
}

const rtpPeriod int64 = 1 << 32
const halfPeriod int64 = 1 << 31

// UnwrapRTPTimestamps expands RFC-style 32-bit wraparound. It refuses exact
// half-period jumps because +half and -half are equally plausible.
func UnwrapRTPTimestamps(values []uint32) ([]int64, error) {
	out := make([]int64, 0, len(values))
	if len(values) == 0 {
		return out, nil
	}
	var expanded int64 = int64(values[0])
	out = append(out, expanded)
	for i := 1; i < len(values); i++ {
		low := uint32(expanded)
		diff := int64(values[i]) - int64(low)
		if diff == halfPeriod || diff == -halfPeriod {
			return nil, media.E(media.CodeRTPAmbiguous, "rtp.unwrap", nil).WithFrame(i + 1).WithOffset(int64(values[i]))
		}
		if diff > halfPeriod {
			diff -= rtpPeriod
		} else if diff < -halfPeriod {
			diff += rtpPeriod
		}
		if math.Abs(float64(diff)) >= float64(halfPeriod) {
			return nil, media.E(media.CodeRTPAmbiguous, "rtp.unwrap", nil).WithFrame(i + 1)
		}
		expanded += diff
		out = append(out, expanded)
	}
	return out, nil
}

func ParseAndUnwrapRTPPackets(packets [][]byte) ([]int64, []bool, error) {
	timestamps := make([]uint32, len(packets))
	markers := make([]bool, len(packets))
	for i, pkt := range packets {
		ts, marker, err := ParseRTPPacket(pkt)
		if err != nil {
			return nil, nil, media.AsError(err).WithFrame(i + 1)
		}
		timestamps[i] = ts
		markers[i] = marker
	}
	expanded, err := UnwrapRTPTimestamps(timestamps)
	if err != nil {
		return nil, nil, err
	}
	return expanded, markers, nil
}
