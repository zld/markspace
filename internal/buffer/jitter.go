package buffer

import (
	"fmt"
	"sort"

	"mediarc/internal/ingest"
	"mediarc/internal/model"
)

// PushStatus describes how the jitter buffer treated one packet.
type PushStatus string

const (
	StatusInOrder    PushStatus = "in_order"
	StatusOutOfOrder PushStatus = "out_of_order"
	StatusLate       PushStatus = "late"
	StatusDuplicate  PushStatus = "duplicate"
	StatusExpired    PushStatus = "expired"
)

// Stats contains deterministic buffer counters.
type Stats struct {
	Received     int   `json:"received"`
	Accepted     int   `json:"accepted"`
	Duplicate    int   `json:"duplicate_packets"`
	OutOfOrder   int   `json:"out_of_order_packets"`
	Late         int   `json:"late_packets"`
	Expired      int   `json:"expired_packets"`
	Lost         int   `json:"lost_packets"`
	JitterEvents int   `json:"jitter_events"`
	JitterTicks  int64 `json:"jitter_ticks"`
	WindowSize   int   `json:"window_size"`
}

// Item is one buffered RTP packet keyed by an extended sequence number.
type Item struct {
	ExtSequence uint64
	Stored      ingest.StoredPacket
	Status      PushStatus
}

// Gap marks a missing RTP sequence interval after a window closes.
type Gap struct {
	FromExt uint64 `json:"from_ext"`
	ToExt   uint64 `json:"to_ext"`
	Count   int    `json:"count"`
}

// JitterBuffer reorders RTP packets by sequence and timestamp. Sequence
// numbers are extended to 64 bits so uint16 wrap does not break ordering.
type JitterBuffer struct {
	Window        int
	LateThreshold int
	ClockRate     uint32
	faults        *model.Collector
	items         map[uint64]Item
	maxExt        uint64
	have          bool
	lastArrival   int64
	lastTS        uint32
	stats         Stats
}

// New creates a buffer. window bounds retained sequence span; lateThreshold
// distinguishes ordinary reordering from late arrival.
func New(window, lateThreshold int, clockRate uint32, faults *model.Collector) *JitterBuffer {
	if window <= 0 {
		window = 64
	}
	if lateThreshold <= 0 || lateThreshold >= window {
		lateThreshold = window / 4
		if lateThreshold < 1 {
			lateThreshold = 1
		}
	}
	if faults == nil {
		faults = &model.Collector{}
	}
	return &JitterBuffer{Window: window, LateThreshold: lateThreshold, ClockRate: clockRate,
		faults: faults, items: make(map[uint64]Item), stats: Stats{WindowSize: window}}
}

// Stats returns a copy of counters.
func (b *JitterBuffer) Stats() Stats { return b.stats }

// Push inserts a packet using zero arrival time.
func (b *JitterBuffer) Push(p ingest.StoredPacket) PushStatus {
	return b.PushAt(p, 0)
}

// PushAt inserts a packet. arrivalTicks may be a deterministic arrival clock;
// when non-zero it contributes to RFC-style interarrival jitter events.
func (b *JitterBuffer) PushAt(p ingest.StoredPacket, arrivalTicks int64) PushStatus {
	b.stats.Received++
	seq := p.Packet.SequenceNumber
	ext := b.extend(seq)
	if _, exists := b.items[ext]; exists {
		b.stats.Duplicate++
		b.faults.Add(model.Fault{Kind: model.FaultDuplicatePacket, RecordIndex: int64(seq),
			Offset: p.Offset, Message: "duplicate RTP sequence", Recoverable: true, Recovered: true})
		return StatusDuplicate
	}

	distance := uint64(0)
	if b.have && ext < b.maxExt {
		distance = b.maxExt - ext
	}
	status := StatusInOrder
	switch {
	case distance > uint64(b.Window):
		status = StatusExpired
		b.stats.Expired++
		b.stats.Late++
		b.faults.Add(model.Fault{Kind: model.FaultLatePacket, RecordIndex: int64(seq), Offset: p.Offset,
			Actual: fmt.Sprint(distance), Expected: fmt.Sprint(b.Window),
			Message: "RTP packet arrived after window closure", Recoverable: false})
		return status
	case distance > uint64(b.LateThreshold):
		status = StatusLate
		b.stats.Late++
		b.stats.OutOfOrder++
		b.stats.JitterEvents++
		b.faults.Add(model.Fault{Kind: model.FaultLatePacket, RecordIndex: int64(seq), Offset: p.Offset,
			Actual: fmt.Sprint(distance), Expected: fmt.Sprint(b.LateThreshold),
			Message: "RTP packet is late but inside recovery window", Recoverable: true, Recovered: true})
	case distance > 0:
		status = StatusOutOfOrder
		b.stats.OutOfOrder++
		b.stats.JitterEvents++
		b.faults.Add(model.Fault{Kind: model.FaultOutOfOrder, RecordIndex: int64(seq), Offset: p.Offset,
			Actual: fmt.Sprint(distance), Message: "RTP packet out of order", Recoverable: true, Recovered: true})
	}

	b.observeJitter(p.Packet.Timestamp, arrivalTicks)
	b.items[ext] = Item{ExtSequence: ext, Stored: p, Status: status}
	b.stats.Accepted++
	if !b.have || ext > b.maxExt {
		b.maxExt = ext
	}
	b.have = true
	return status
}

func (b *JitterBuffer) extend(seq uint16) uint64 {
	if !b.have {
		b.maxExt = uint64(seq)
		b.have = true
		return b.maxExt
	}
	ext := (b.maxExt &^ 0xffff) | uint64(seq)
	const half = uint64(1) << 15
	if ext+half < b.maxExt {
		ext += 1 << 16
	} else if b.maxExt+half < ext {
		ext -= 1 << 16
	}
	return ext
}

func (b *JitterBuffer) observeJitter(ts uint32, arrival int64) {
	if b.lastArrival != 0 && arrival != 0 {
		d := (arrival - b.lastArrival) - int64(ts-b.lastTS)
		if d < 0 {
			d = -d
		}
		if d > int64(b.LateThreshold) {
			b.stats.JitterEvents++
			b.stats.JitterTicks += d
		}
	}
	b.lastArrival = arrival
	b.lastTS = ts
}

// Flush closes the complete sequence window and returns unique packets in
// extended sequence order plus all missing sequence gaps.
func (b *JitterBuffer) Flush() ([]Item, []Gap) {
	if !b.have {
		return nil, nil
	}
	keys := make([]uint64, 0, len(b.items))
	var minExt uint64 = ^uint64(0)
	for k := range b.items {
		keys = append(keys, k)
		if k < minExt {
			minExt = k
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	items := make([]Item, 0, len(keys))
	var gaps []Gap
	expected := minExt
	for _, k := range keys {
		if k > expected {
			gap := Gap{FromExt: expected, ToExt: k - 1, Count: int(k - expected)}
			gaps = append(gaps, gap)
			b.stats.Lost += gap.Count
			for seq := expected; seq < k; seq++ {
				b.faults.Add(model.Fault{Kind: model.FaultRTPLoss, RecordIndex: int64(uint16(seq)),
					Message: "RTP sequence missing at window close", Recoverable: true, Recovered: true})
			}
		}
		items = append(items, b.items[k])
		expected = k + 1
	}
	return items, gaps
}
