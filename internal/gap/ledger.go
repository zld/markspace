// Package gap records missing and late media intervals without inventing
// samples. Every interval carries both sample and common-tick coordinates.
package gap

import (
	"sync"

	"mediacheck/internal/timebase"
)

type Kind string

const (
	KindMissing Kind = "missing"
	KindLate    Kind = "late_outside_window"
)

// Gap is a half-open sample interval [StartSample, EndSample). Seq values are
// unwrapped 16-bit RTP sequence numbers, so they remain monotonic across wrap.
type Gap struct {
	Index        int    `json:"index"`
	Kind         Kind   `json:"kind"`
	StartSeq     int64  `json:"start_seq"`
	EndSeq       int64  `json:"end_seq"`
	PacketCount  int    `json:"packet_count"`
	StartSample  int64  `json:"start_sample"`
	EndSample    int64  `json:"end_sample"`
	StartTick    int64  `json:"start_tick"`
	EndTick      int64  `json:"end_tick"`
	SourceOffset int64  `json:"source_offset,omitempty"`
	Reason       string `json:"reason"`
}

// Ledger is the concurrency-safe authoritative gap list for one track.
type Ledger struct {
	mu         sync.RWMutex
	track      string
	sampleRate uint32
	gaps       []Gap
}

func NewLedger(track string, sampleRate uint32) *Ledger {
	return &Ledger{track: track, sampleRate: sampleRate}
}

// Record adds a gap. endSample is exclusive. Sequence range is inclusive.
func (l *Ledger) Record(kind Kind, startSeq, endSeq int64, packets int, startSample, endSample int64, reason string) (Gap, error) {
	if endSample < startSample {
		return Gap{}, timebase.NewError(timebase.CodeInvalidArgument, "gap end before start")
	}
	startTick, err := timebase.SamplesToTicks(startSample, l.sampleRate, timebase.TickRate)
	if err != nil {
		return Gap{}, err
	}
	endTick, err := timebase.SamplesToTicks(endSample, l.sampleRate, timebase.TickRate)
	if err != nil {
		return Gap{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	g := Gap{
		Index:       len(l.gaps),
		Kind:        kind,
		StartSeq:    startSeq,
		EndSeq:      endSeq,
		PacketCount: packets,
		StartSample: startSample,
		EndSample:   endSample,
		StartTick:   startTick,
		EndTick:     endTick,
		Reason:      reason,
	}
	l.gaps = append(l.gaps, g)
	return g, nil
}

func (l *Ledger) RecordMissing(startSeq, endSeq int64, packets int, startSample, endSample int64, reason string) (Gap, error) {
	return l.Record(KindMissing, startSeq, endSeq, packets, startSample, endSample, reason)
}

func (l *Ledger) RecordLate(seq int64, sample int64, sampleCount int, reason string) (Gap, error) {
	return l.Record(KindLate, seq, seq, 1, sample, sample+int64(sampleCount), reason)
}

// Gaps returns a copy suitable for manifests and tests.
func (l *Ledger) Gaps() []Gap {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Gap, len(l.gaps))
	copy(out, l.gaps)
	return out
}

func (l *Ledger) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.gaps)
}

// MissingPackets returns the total packet count across recorded gaps.
func (l *Ledger) MissingPackets() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	n := 0
	for _, g := range l.gaps {
		n += g.PacketCount
	}
	return n
}
