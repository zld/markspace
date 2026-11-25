package rtp

import (
	"time"
)

// Pacer models variable network bandwidth for an offline batch. It only
// controls *when* packets are handed to the ingest: slower bandwidth
// inserts longer waits. Packet bytes, ordering, dedup, gaps and therefore
// every digest are unaffected. A zero or negative Interval disables waits
// entirely so tests stay fast and deterministic.
type Pacer struct {
	// Interval is the wait applied between packets.
	Interval time.Duration
	// Waits counts how many pacing delays were applied.
	Waits int
	sleep func(time.Duration)
}

// NewPacer builds a pacer. A nil sleep uses time.Sleep.
func NewPacer(interval time.Duration) *Pacer {
	return &Pacer{Interval: interval, sleep: time.Sleep}
}

// Wait applies one pacing delay. It is safe on a zero-value Pacer.
func (p *Pacer) Wait() {
	if p == nil || p.Interval <= 0 {
		return
	}
	p.Waits++
	if p.sleep != nil {
		p.sleep(p.Interval)
	}
}

// SetBandwidth re-tunes the interval to emulate a bandwidth change. The
// relation is inverse: lower bandwidth => longer inter-packet wait.
func (p *Pacer) SetBandwidth(interval time.Duration) {
	if p == nil {
		return
	}
	p.Interval = interval
}
