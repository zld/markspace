// Package timeline expands timestamp wraparound, corrects clock drift, tracks
// watermarks, and performs deterministic checkpoint recomputation.
package timeline

import (
	"fmt"

	"github.com/pion/rtp"

	"github.com/zld/mediarc/internal/model"
)

// UnwrapState is the serializable wraparound state for one track.
type UnwrapState struct {
	TrackID       uint32 `json:"track_id"`
	Started       bool   `json:"started"`
	LastRaw       uint64 `json:"last_raw"`
	LastUnwrapped int64  `json:"last_unwrapped"`
	TimestampBits int    `json:"timestamp_bits"`
}

// Unwrapper expands fixed-width timestamps by choosing the nearest modular
// continuation. The 32-bit case is the same timestamp domain carried by
// pion/rtp Header.Timestamp.
type Unwrapper struct {
	states map[uint32]*UnwrapState
}

// NewUnwrapper creates an empty per-track unwrapper.
func NewUnwrapper() *Unwrapper {
	return &Unwrapper{states: map[uint32]*UnwrapState{}}
}

// States returns deterministic checkpoint state copies.
func (u *Unwrapper) States() map[uint32]UnwrapState {
	out := make(map[uint32]UnwrapState, len(u.states))
	for id, st := range u.states {
		out[id] = *st
	}
	return out
}

// Restore replaces all unwrap states from a checkpoint.
func (u *Unwrapper) Restore(states map[uint32]UnwrapState) {
	u.states = make(map[uint32]*UnwrapState, len(states))
	for id, st := range states {
		cp := st
		u.states[id] = &cp
	}
}

// UnwrapRaw expands one fixed-width timestamp. Events for a track must be
// supplied in continuous container/sample order; late insertion is handled by
// recomputing an ordered prefix rather than mutating this state out of order.
func (u *Unwrapper) UnwrapRaw(trackID uint32, raw uint64, bits int) (int64, UnwrapState, error) {
	if bits == 0 {
		bits = 32
	}
	if bits < 8 || bits > 64 {
		return 0, UnwrapState{}, &model.ConfigError{Field: "timestamp_bits", Value: bits, Err: fmt.Errorf("must be in [8,64]")}
	}
	st := u.states[trackID]
	if st == nil {
		st = &UnwrapState{TrackID: trackID, TimestampBits: bits}
		u.states[trackID] = st
	}
	if st.TimestampBits == 0 {
		st.TimestampBits = bits
	}
	if st.TimestampBits != bits {
		return 0, *st, fmt.Errorf("track %d timestamp width changed: %d != %d", trackID, bits, st.TimestampBits)
	}

	if !st.Started {
		st.Started = true
		st.LastRaw = raw
		if bits == 64 {
			st.LastUnwrapped = int64(raw) //nolint:gosec // fixture/config constrains values in tests
		} else {
			st.LastUnwrapped = int64(raw)
		}
		return st.LastUnwrapped, *st, nil
	}

	delta := modularDelta(raw, st.LastRaw, bits)
	st.LastUnwrapped += delta
	st.LastRaw = raw
	return st.LastUnwrapped, *st, nil
}

// UnwrapRTPPacket expands a decoded pion/rtp packet timestamp. It makes the
// reuse of pion/rtp's wire timestamp type explicit.
func (u *Unwrapper) UnwrapRTPPacket(trackID uint32, packet *rtp.Packet) (int64, error) {
	if packet == nil {
		return 0, fmt.Errorf("nil RTP packet")
	}
	unwrapped, _, err := u.UnwrapRaw(trackID, uint64(packet.Header.Timestamp), 32)
	return unwrapped, err
}

func modularDelta(next, prev uint64, bits int) int64 {
	if bits == 64 {
		return int64(next - prev) //nolint:gosec // defined two's-complement delta
	}
	modulus := uint64(1) << bits
	half := int64(modulus >> 1)
	d := int64(next) - int64(prev)
	if d > half {
		d -= int64(modulus)
	} else if d < -half {
		d += int64(modulus)
	}
	return d
}

// UnwrapEvents applies per-track continuous order and converts the result to
// integer nanoseconds using each event's own timescale.
func UnwrapEvents(events []model.RawEvent) ([]Event, map[uint32]UnwrapState, error) {
	u := NewUnwrapper()
	out := make([]Event, 0, len(events))
	for _, raw := range events {
		ticks, _, err := u.UnwrapRaw(raw.TrackID, raw.RawTimestamp, raw.TimestampBits)
		if err != nil {
			return nil, nil, err
		}
		sourceNS, err := model.SignedToNS(ticks, raw.Timescale)
		if err != nil {
			return nil, nil, &model.ParseError{
				TrackID:    int(raw.TrackID),
				BoxType:    "mdhd/stts",
				ByteOffset: raw.BoxOffset,
				Err:        err,
			}
		}
		out = append(out, Event{Raw: raw, UnwrappedTicks: ticks, SourceNS: sourceNS, TimeNS: sourceNS})
	}
	return out, u.States(), nil
}
