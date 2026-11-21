// Package rtp synthesizes deterministic VP8 RTP packets for loopback tests.
package rtp

import (
	"fmt"
	"time"

	"github.com/pion/rtp"
)

const (
	// PayloadType is the VP8 payload type negotiated by the demo.
	PayloadType uint8 = 96
	// SSRC identifies the synthetic engine-side stream.
	SSRC uint32 = 0x10C0FFEE
	// ClockRate is the RTP video clock.
	ClockRate uint32 = 90000
	// DefaultSamplesPerPacket is a 20 ms video frame step in 90 kHz ticks.
	DefaultSamplesPerPacket uint32 = 1800
)

// Config controls synthetic packet generation.
type Config struct {
	Packets    int
	Interval   time.Duration
	DropEvery  int
	DelayEvery int
	Delay      time.Duration
	// StartSequence overrides the first sequence number; useful to exercise
	// 16-bit wrap-around.
	StartSequence uint16
	// StartTimestamp overrides the first timestamp; useful to exercise
	// 32-bit wrap-around.
	StartTimestamp uint32
	// SamplesPerPacket overrides the timestamp step. Zero derives the step
	// from Interval, so ideal pacing produces no timestamp-vs-arrival drift.
	SamplesPerPacket uint32
}

// Packet is a generated RTP packet and the timing metadata the source applied.
type Packet struct {
	RTP     *rtp.Packet
	Index   int
	SendAt  time.Time
	Skip    bool
	Delayed bool
}

// Schedule describes the generator outcome, computed without wall-clock
// execution so it can be asserted in tests.
type Schedule struct {
	Generated    int
	Skipped      int
	Delayed      int
	StartSeq     uint16
	StartTS      uint32
	EndSeq       uint16
	EndTS        uint32
	SpanDuration time.Duration
}

// BuildSchedule precomputes every packet for the given configuration.
func BuildSchedule(cfg Config) ([]Packet, Schedule, error) {
	if cfg.Packets < 1 {
		return nil, Schedule{}, fmt.Errorf("packets must be positive, got %d", cfg.Packets)
	}
	if cfg.Interval <= 0 {
		return nil, Schedule{}, fmt.Errorf("interval must be positive, got %s", cfg.Interval)
	}
	if cfg.DropEvery == 1 || cfg.DropEvery < 0 {
		return nil, Schedule{}, fmt.Errorf("drop-every must be 0 or >= 2, got %d", cfg.DropEvery)
	}
	if cfg.DelayEvery == 1 || cfg.DelayEvery < 0 {
		return nil, Schedule{}, fmt.Errorf("delay-every must be 0 or >= 2, got %d", cfg.DelayEvery)
	}
	if cfg.Delay < 0 {
		return nil, Schedule{}, fmt.Errorf("delay must not be negative")
	}

	step := cfg.SamplesPerPacket
	if step == 0 {
		step = SamplesForInterval(cfg.Interval)
	}
	if step == 0 {
		return nil, Schedule{}, fmt.Errorf("interval %s is shorter than one 90 kHz tick", cfg.Interval)
	}

	out := make([]Packet, 0, cfg.Packets)
	sched := Schedule{Generated: cfg.Packets, StartSeq: cfg.StartSequence, StartTS: cfg.StartTimestamp}
	origin := time.Unix(0, 0).UTC()

	for i := 0; i < cfg.Packets; i++ {
		seq := cfg.StartSequence + uint16(i)      //nolint:gosec // intentional 16-bit wrap
		ts := cfg.StartTimestamp + uint32(i)*step //nolint:gosec // intentional 32-bit wrap
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    PayloadType,
				SequenceNumber: seq,
				Timestamp:      ts,
				SSRC:           SSRC,
				Marker:         i == cfg.Packets-1,
			},
			Payload: payloadFor(i),
		}
		p := Packet{
			RTP:    pkt,
			Index:  i,
			SendAt: origin.Add(time.Duration(i) * cfg.Interval),
		}
		if cfg.DropEvery > 0 && (i+1)%cfg.DropEvery == 0 {
			p.Skip = true
			sched.Skipped++
		}
		if cfg.DelayEvery > 0 && (i+1)%cfg.DelayEvery == 0 && cfg.Delay > 0 {
			p.Delayed = true
			p.SendAt = p.SendAt.Add(cfg.Delay)
			sched.Delayed++
		}
		out = append(out, p)
	}
	sched.EndSeq = out[len(out)-1].RTP.SequenceNumber
	sched.EndTS = out[len(out)-1].RTP.Timestamp
	sched.SpanDuration = out[len(out)-1].SendAt.Sub(out[0].SendAt)
	return out, sched, nil
}

// SamplesForInterval converts a wall-clock interval to 90 kHz ticks.
func SamplesForInterval(d time.Duration) uint32 {
	if d <= 0 {
		return 0
	}
	return uint32(d * time.Duration(ClockRate) / time.Second) //nolint:gosec // bounded by interval validation
}

// payloadFor produces a small VP8-looking payload. The first byte carries the
// frame tag with S=1, PID=0, so receivers accept it as a keyframe part.
func payloadFor(index int) []byte {
	frame := byte(index % 64)
	return []byte{0x10, frame, 0x00, 0x00}
}

// Wrap helpers used by tests and metrics.

// SequenceDelta returns the signed forward distance from prev to next under
// 16-bit modular arithmetic.
func SequenceDelta(prev, next uint16) int {
	return int(int16(next - prev))
}

// TimestampDelta returns the signed forward distance in 32-bit modular
// arithmetic.
func TimestampDelta(prev, next uint32) int64 {
	return int64(int32(next - prev))
}

// WrapSequence returns seq advanced by n with 16-bit wrap-around.
func WrapSequence(seq uint16, n int) uint16 {
	return uint16(int(seq) + n)
}

// WrapTimestamp returns ts advanced by n samples with 32-bit wrap-around.
func WrapTimestamp(ts uint32, n int) uint32 {
	return uint32(int64(ts) + int64(n))
}
