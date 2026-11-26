// Package rtp reconciles parsed RTP sample packets onto the shared rational
// time base. Timestamps are unwrapped only at determinable 32-bit wrap points,
// and each packet contributes its own real sample count rather than an average
// frame rate.
package rtp

import (
	"github.com/pion/rtp"

	"example.com/mediavault/internal/clock"
	"example.com/mediavault/internal/fault"
)

// MaxDriftPPM is the isolation threshold from the acceptance contract.
const MaxDriftPPM int64 = 500

// Limits keeps the timeline checks independent of the WAV reader.
type Limits struct {
	MaxPacketSamples int64
	MaxPackets       int
}

// DefaultLimits matches the 4 MiB working-memory envelope.
func DefaultLimits() Limits { return Limits{MaxPacketSamples: 1 << 20, MaxPackets: 1 << 20} }

// Packet is one reconciled RTP payload.
type Packet struct {
	Sequence   uint16 `json:"sequence"`
	RawStamp   uint32 `json:"raw_stamp"`
	Stamp      uint64 `json:"stamp"`
	Wrapped    bool   `json:"wrapped"`
	Samples    int64  `json:"samples"`
	Position   int64  `json:"position"`
	Correction int64  `json:"correction"`
}

// Timeline is the reconciled RTP result for one track.
type Timeline struct {
	Packets     []Packet `json:"packets"`
	Wraps       int      `json:"wraps"`
	SampleRate  clock.Rate `json:"sample_rate"`
	ClockRate   uint32     `json:"clock_rate"`
	TotalSamples int64     `json:"total_samples"`
	DriftPPM    int64      `json:"drift_ppm"`
	Corrections int64      `json:"corrections"`
}

// Convert maps an extended RTP timestamp at clockRate ticks per second to a
// sample position on rate. Every step is overflow checked.
func Convert(extended uint64, clockRate uint32, rate clock.Rate) (int64, error) {
	if clockRate == 0 {
		return 0, fault.New(fault.CodeRTPOverflow, fault.StageNormalized, -1, "zero clock rate")
	}
	if !rate.Valid() {
		return 0, fault.New(fault.CodeRTPOverflow, fault.StageNormalized, -1, "invalid sample rate")
	}
	if extended > uint64(^uint64(0)>>1) {
		return 0, fault.New(fault.CodeRTPOverflow, fault.StageNormalized, -1, "timestamp %d exceeds int64", extended)
	}
	// position = extended * rate.Num / (clockRate * rate.Den)
	num := uint64(rate.Num) * extended
	if rate.Num != 0 && num/uint64(rate.Num) != extended {
		return 0, fault.New(fault.CodeRTPOverflow, fault.StageNormalized, -1, "timestamp %d overflow converting to sample rate", extended)
	}
	den := uint64(clockRate) * uint64(rate.Den)
	if uint64(clockRate) != 0 && den/uint64(clockRate) != uint64(rate.Den) {
		return 0, fault.New(fault.CodeRTPOverflow, fault.StageNormalized, -1, "clock rate %d overflows with denominator %d", clockRate, rate.Den)
	}
	pos := num / den
	if pos > uint64(^uint64(0)>>1) {
		return 0, fault.New(fault.CodeRTPOverflow, fault.StageNormalized, -1, "sample position %d exceeds int64", pos)
	}
	return int64(pos), nil
}

// Reconcile walks raw RTP packets in sequence order. frameBytes is the size of
// one interleaved frame so that a variable payload yields its real sample
// count; a payload that is not frame aligned is rejected outright.
func Reconcile(raw []rtp.Packet, rate clock.Rate, clockRate uint32, frameBytes int, limits Limits) (*Timeline, error) {
	if frameBytes <= 0 {
		return nil, fault.New(fault.CodeFormat, fault.StageNormalized, -1, "invalid frame size %d", frameBytes)
	}
	if len(raw) > limits.MaxPackets {
		return nil, fault.New(fault.CodeLimit, fault.StageNormalized, -1, "packet count %d exceeds limit %d", len(raw), limits.MaxPackets)
	}
	if len(raw) == 0 {
		return nil, fault.New(fault.CodeRTPSynth, fault.StageNormalized, -1, "no RTP packets")
	}
	uw := clock.NewUnwrapper()
	tl := &Timeline{SampleRate: rate, ClockRate: clockRate}
	var expected int64 // cumulative real sample count
	var firstPos int64
	var haveFirst bool
	for i, p := range raw {
		if len(p.Payload)%frameBytes != 0 {
			return nil, fault.New(fault.CodeFrameAlign, fault.StageNormalized, -1,
				"packet %d payload %d bytes not frame aligned to %d", i, len(p.Payload), frameBytes)
		}
		samples := int64(len(p.Payload) / frameBytes)
		if samples > limits.MaxPacketSamples {
			return nil, fault.New(fault.CodeLimit, fault.StageNormalized, -1,
				"packet %d has %d samples over limit", i, samples)
		}
		stamp, wrapped, err := uw.Unwrap(p.Timestamp)
		if err != nil {
			return nil, err
		}
		if wrapped {
			tl.Wraps++
		}
		pos, err := Convert(stamp, clockRate, rate)
		if err != nil {
			return nil, err
		}
		if !haveFirst {
			haveFirst = true
			firstPos = pos
			expected = 0
		}
		// Position of this packet relative to the first packet, in samples.
		rel := pos - firstPos
		corr := rel - expected
		if corr < 0 {
			corr = -corr
		}
		tl.Corrections += corr
		tl.Packets = append(tl.Packets, Packet{
			Sequence: p.SequenceNumber, RawStamp: p.Timestamp, Stamp: stamp,
			Wrapped: wrapped, Samples: samples, Position: rel, Correction: corr,
		})
		expected += samples
	}
	tl.TotalSamples = expected
	if len(tl.Packets) > 1 {
		// Drift is measured only across the span that real timestamps actually
		// cover: from the first timestamp to the last, against the sum of the
		// samples carried by the packets before the last. The final packet's own
		// payload has no later timestamp to compare with, so including it would
		// bias the ratio toward zero. No average frame rate is ever assumed.
		last := tl.Packets[len(tl.Packets)-1]
		timestamped := expected - last.Samples
		if timestamped > 0 {
			ppm, err := clock.PPMDelta(last.Position, timestamped)
			if err != nil {
				return nil, err
			}
			tl.DriftPPM = ppm
			if ppm > MaxDriftPPM || ppm < -MaxDriftPPM {
				return nil, fault.New(fault.CodeRTPDrift, fault.StageNormalized, -1,
					"drift %d ppm exceeds %d ppm over %d timestamped samples", ppm, MaxDriftPPM, timestamped)
			}
		}
	}
	return tl, nil
}
