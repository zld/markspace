package timeline

import (
	"sort"

	"mediaforge/internal/clock"
)

// Event is one RTP packet placed on the media timeline.
type Event struct {
	Seq       uint16
	ExtSeq    int64
	Timestamp uint32
	ExtTS     int64
	Arrival   int
	Late      bool
	// Duplicate marks an arrival whose sequence number was already seen.
	Duplicate bool
	Payload   []byte
	// Samples is the number of audio frames carried by the payload. It comes
	// from the payload length, never from an average packet spacing.
	Samples int64
	// AffectedStart and AffectedEnd bound the sample interval that must be
	// recomputed when this packet arrived late. End is exclusive.
	AffectedStart int64
	AffectedEnd   int64
}

// Gap is a contiguous run of missing extended sequence numbers.
type Gap struct {
	StartSeq int64
	EndSeq   int64 // exclusive
}

// Summary is the deterministic result of finalizing a replay manager.
type Summary struct {
	Events     []Event
	Gaps       []Gap
	Unique     int
	Duplicates int
	Late       int
	Loss       int64
}

// Manager buffers events by sequence number and exposes a monotonic
// watermark. All decisions depend only on the input stream, so repeated runs
// produce identical ordering, statistics and output.
type Manager struct {
	rate  int64
	wm    *Watermark
	seen  map[int64]*Event
	all   []*Event
	order []int64

	baseSeq    uint16
	baseTS     uint32
	haveBase   bool
	lastSeq    uint16
	lastExtSeq int64
	lastTS     uint32
	lastExtTS  int64
	minSeq     int64
	nextArrive int

	duplicates int
	late       int
}

// NewManager creates a manager for a stream with the given RTP clock rate.
func NewManager(rate int64, delaySamples int64) *Manager {
	if rate <= 0 {
		rate = 48000
	}
	if delaySamples < 0 {
		delaySamples = 0
	}
	return &Manager{
		rate:   rate,
		minSeq: 0,
		wm:     NewWatermark(clock.NewRat(delaySamples, rate)),
		seen:   make(map[int64]*Event),
	}
}

// Seed fixes the unwrapping origin so callers can expand timestamps from the
// first packet in sequence order rather than the first in arrival order.
func (m *Manager) Seed(baseSeq uint16, baseTS uint32) {
	if m.haveBase {
		return
	}
	m.baseSeq, m.baseTS, m.haveBase = baseSeq, baseTS, true
	m.lastSeq, m.lastExtSeq = baseSeq, 0
	m.lastTS, m.lastExtTS = baseTS, 0
}

// Push adds one packet. Duplicate sequence numbers are counted and ignored.
// A packet whose event time precedes the watermark is marked late and its
// affected sample interval is reported, without changing earlier events.
func (m *Manager) Push(seq uint16, ts uint32, payload []byte, samples int64) Event {
	arrival := m.nextArrive
	m.nextArrive++
	if !m.haveBase {
		m.baseSeq, m.baseTS, m.haveBase = seq, ts, true
		m.lastSeq, m.lastTS = seq, ts
	}
	// Extend both counters in arrival order. Extending relative to the newest
	// packet (not the first) keeps a late packet from a previous wrap cycle on
	// the correct cycle while still allowing it to sort before newer packets.
	extSeq := m.lastExtSeq + int64(int16(seq-m.lastSeq))
	extTS := m.lastExtTS + int64(int32(ts-m.lastTS))
	m.lastSeq, m.lastExtSeq = seq, extSeq
	m.lastTS, m.lastExtTS = ts, extTS
	// Remember the lowest sequence number seen so loss is counted over the
	// full observed window even when the first packets arrive late.
	if extSeq < m.minSeq {
		m.minSeq = extSeq
	}
	if samples < 0 {
		samples = 0
	}
	ev := Event{
		Seq: seq, ExtSeq: extSeq, Timestamp: ts, ExtTS: extTS,
		Arrival: arrival, Payload: payload, Samples: samples,
	}
	if prev, dup := m.seen[extSeq]; dup {
		m.duplicates++
		return Event{Seq: prev.Seq, ExtSeq: prev.ExtSeq, Timestamp: prev.Timestamp,
			ExtTS: prev.ExtTS, Arrival: prev.Arrival, Late: prev.Late, Duplicate: true,
			Samples: prev.Samples, AffectedStart: prev.AffectedStart, AffectedEnd: prev.AffectedEnd}
	}
	ev.Late = m.wm.IsLate(clock.NewTime(extTS, m.rate))
	if ev.Late {
		m.late++
		ev.AffectedStart = clock.MulDivRound(extTS, m.rate, m.rate)
		ev.AffectedEnd = ev.AffectedStart + samples
	}
	m.seen[extSeq] = &ev
	m.all = append(m.all, &ev)
	m.order = append(m.order, extSeq)
	m.wm.Observe(clock.NewTime(extTS, m.rate))
	return ev
}

// WatermarkSeconds returns the current horizon, or (0,false) when empty.
func (m *Manager) WatermarkSeconds() (clock.Rat, bool) { return m.wm.Horizon() }

// Summary finalizes the stream: events sorted by sequence number with
// duplicates removed, plus every missing sequence range.
func (m *Manager) Summary() Summary {
	s := Summary{Duplicates: m.duplicates, Late: m.late}
	if len(m.all) == 0 {
		return s
	}
	minSeq, maxSeq := m.minSeq, m.order[0]
	for _, e := range m.all {
		if e.ExtSeq > maxSeq {
			maxSeq = e.ExtSeq
		}
	}
	sorted := append([]*Event(nil), m.all...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].ExtSeq == sorted[j].ExtSeq {
			return sorted[i].Arrival < sorted[j].Arrival
		}
		return sorted[i].ExtSeq < sorted[j].ExtSeq
	})
	s.Events = make([]Event, len(sorted))
	for i, e := range sorted {
		s.Events[i] = *e
	}
	s.Unique = len(sorted)
	s.Loss = (maxSeq - minSeq + 1) - int64(len(sorted))
	for seq := minSeq; seq <= maxSeq; {
		if _, ok := m.seen[seq]; ok {
			seq++
			continue
		}
		start := seq
		for seq <= maxSeq {
			if _, ok := m.seen[seq]; ok {
				break
			}
			seq++
		}
		s.Gaps = append(s.Gaps, Gap{StartSeq: start, EndSeq: seq})
	}
	return s
}

// EventAt returns the event for an extended sequence number, if present.
func (m *Manager) EventAt(extSeq int64) (Event, bool) {
	e, ok := m.seen[extSeq]
	if !ok {
		return Event{}, false
	}
	return *e, true
}
