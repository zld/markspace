package rtp

import (
	"fmt"

	"mediasync/internal/model"
)

// TimestampExtender unfolds RTP timestamps of a configurable bit width. It
// uses the same closest-extension rule as sequence numbers and converts the
// extended value to the kernel time base using the track timescale.
type TimestampExtender struct {
	ext       *Extender
	timescale uint32
}

// NewTimestampExtender builds an extender for a 1..63 bit timestamp field.
// RTP uses 32 bits, which is the default at call sites.
func NewTimestampExtender(bits uint, timescale uint32) (*TimestampExtender, error) {
	if timescale == 0 {
		return nil, fmt.Errorf("timescale must be positive")
	}
	e, err := NewExtender(bits)
	if err != nil {
		return nil, err
	}
	return &TimestampExtender{ext: e, timescale: timescale}, nil
}

// Bits returns the configured timestamp width.
func (t *TimestampExtender) Bits() uint { return t.ext.Bits() }

// Extend returns the extended raw tick value and whether it moved backwards.
func (t *TimestampExtender) Extend(raw uint32) (int64, bool) {
	return t.ext.Extend(uint64(raw))
}

// Nanos converts an extended tick value to the kernel time base.
func (t *TimestampExtender) Nanos(ext int64) (model.Nanos, error) {
	return model.ToNanos(ext, t.timescale)
}

// ExtendNanos combines extension and conversion.
func (t *TimestampExtender) ExtendNanos(raw uint32) (ext int64, ts model.Nanos, backward bool, err error) {
	ext, backward = t.Extend(raw)
	ts, err = t.Nanos(ext)
	return
}

// Snapshot serializes the extender state.
type TimestampSnapshot struct {
	LastRaw uint64 `json:"last_raw"`
	LastExt int64  `json:"last_ext"`
	Init    bool   `json:"init"`
}

// Snapshot captures the timestamp extender state.
func (t *TimestampExtender) Snapshot() TimestampSnapshot {
	return TimestampSnapshot{LastRaw: t.ext.lastRaw, LastExt: t.ext.lastExt, Init: t.ext.init}
}

// Restore replaces the timestamp extender state.
func (t *TimestampExtender) Restore(s TimestampSnapshot) {
	t.ext.lastRaw, t.ext.lastExt, t.ext.init = s.LastRaw, s.LastExt, s.Init
}

// ExtenderSnapshot is the serializable state of a sequence extender.
type ExtenderSnapshot struct {
	LastRaw uint64 `json:"last_raw"`
	LastExt int64  `json:"last_ext"`
	Init    bool   `json:"init"`
}

// Snapshot captures the sequence extender state.
func (e *Extender) Snapshot() ExtenderSnapshot {
	return ExtenderSnapshot{LastRaw: e.lastRaw, LastExt: e.lastExt, Init: e.init}
}

// Restore replaces the sequence extender state.
func (e *Extender) Restore(s ExtenderSnapshot) {
	e.lastRaw, e.lastExt, e.init = s.LastRaw, s.LastExt, s.Init
}
