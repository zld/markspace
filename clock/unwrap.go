// Package clock expands 32-bit RTP timestamps into a monotonic clock and
// enforces the continuity rule: a timestamp rollover is expanded only while
// sequence-number continuity holds. Backwards steps and implausible jumps are
// recorded with their original record position and the affected packets are
// placed in isolated ranges instead of silently reordering or dropping them.
package clock

import (
	"time"

	"rtpreplay/media"
)

// AnomalyKind enumerates the continuity violations the unwrapper detects.
type AnomalyKind string

const (
	AnomNone       AnomalyKind = ""
	AnomWrap       AnomalyKind = "wrap"        // 32-bit rollover, continuity held
	AnomBackward   AnomalyKind = "backward"    // timestamp moved backwards
	AnomJump       AnomalyKind = "jump"        // forward step beyond the plausibility window
	AnomSeqGap     AnomalyKind = "seq_gap"     // missing sequence numbers, clock intact
	AnomDuplicate  AnomalyKind = "duplicate"   // repeated sequence number
	AnomSeqRestart AnomalyKind = "seq_restart" // sequence space restarted far away
)

// RFC 3550 style sequence windows.
const (
	maxSeqDropout  = 3000
	maxSeqMisorder = 100
)

// Range is a contiguous run of records in one isolated region.
type Range struct {
	Reason                 AnomalyKind
	StartRecord, EndRecord int
	StartExtSeq, EndExtSeq uint64
}

// Packet is the unwrapped view of one RTP record.
type Packet struct {
	Index     int
	Seq       uint16
	ExtSeq    uint64
	RTP       uint32
	Unwrapped int64 // 32-bit-expanded RTP timestamp; monotonic inside a region
	Arrival   time.Time
	RegionID  int
	Isolated  bool
	Anomaly   AnomalyKind
}

// Unwrapper is per-SSRC state. One instance must never be shared between
// representations: each representation recovers on its own clock.
type Unwrapper struct {
	rate    uint32
	maxJump int64 // ticks (10 s plausibility window)

	have      bool
	lastSeq   uint16
	cycles    uint32
	lastTS    uint32
	unwrapped int64

	region    int
	isolated  bool
	cur       *Range // open isolated range; nil while the region is healthy
	ranges    []Range
	wrapCount int
}

// New constructs an Unwrapper for one of the sample-supported clock rates.
func New(rate uint32) (*Unwrapper, error) {
	if !media.SupportedClockRate(rate) {
		return nil, media.NewError(media.KindUnsupported, "UNSUPPORTED_RATE", "clock", -1,
			"clock rate %d is outside the sample scope", rate)
	}
	return &Unwrapper{rate: rate, maxJump: int64(rate) * 10}, nil
}

// WrapCount reports how many rollovers were expanded under continuity.
func (u *Unwrapper) WrapCount() int { return u.wrapCount }

// IsolatedRanges returns the isolated ranges seen so far.
func (u *Unwrapper) IsolatedRanges() []Range { return append([]Range(nil), u.ranges...) }

// Add feeds one (sequence, timestamp, arrival) observation. The returned
// packet always carries a best-effort unwrapped timestamp; anomaly packets
// are additionally flagged and isolated where continuity cannot be proven.
func (u *Unwrapper) Add(index int, seq, ts uint32, arrival time.Time) (*Packet, error) {
	if !u.have {
		u.have = true
		u.lastSeq = uint16(seq)
		u.lastTS = ts
		u.unwrapped = int64(ts)
		return u.packet(index, uint16(seq), uint64(seq), ts, arrival, AnomNone), nil
	}

	udelta := seq - uint32(u.lastSeq) // uint32 arithmetic = modular distance

	switch {
	case udelta == 0:
		// Duplicate: stream position does not advance; stays in the current
		// region so the surrounding interval is not split.
		p := u.packet(index, u.lastSeq, u.curExtSeq(), ts, arrival, AnomDuplicate)
		p.Unwrapped = u.unwrapped
		return p, nil

	case udelta >= uint32(65536-maxSeqMisorder):
		// Late misordered packet from the past: isolated singleton, state
		// does not advance.
		return u.isolate(index, seq, ts, arrival, AnomBackward), nil

	case udelta > maxSeqDropout:
		// Sequence space restarted far away: no continuity claim possible.
		u.cycles = 0
		u.lastSeq = uint16(seq)
		u.lastTS = ts
		u.unwrapped = int64(ts)
		return u.isolate(index, seq, ts, arrival, AnomSeqRestart), nil
	}

	// Normal forward sequence step (a gap larger than one is flagged later).
	if uint16(seq) < u.lastSeq {
		u.cycles++ // 16-bit sequence rollover
	}
	u.lastSeq = uint16(seq)

	// Classify the timestamp step. Sequence continuity is already proven
	// here. raw is the modular uint32 difference; combining its magnitude
	// with the numeric rise/fall of the raw value disambiguates:
	//   small raw + value fell  -> 32-bit rollover (small true forward step)
	//   small raw + value rose  -> normal forward step
	//   large raw, raw > 2^31   -> backwards injection (magnitude 2^32-raw)
	//   large raw, raw <= 2^31  -> implausible forward jump
	raw := ts - u.lastTS
	var anomaly AnomalyKind
	switch {
	case int64(raw) <= u.maxJump && ts < u.lastTS:
		// Rollover: monotonic accumulation of the small modular step already
		// yields the expanded timestamp; record the boundary crossing.
		u.unwrapped += int64(raw)
		u.wrapCount++
		anomaly = AnomWrap
	case int64(raw) <= u.maxJump:
		u.unwrapped += int64(raw)
	case raw > 0x80000000:
		// Backwards step beyond the plausibility window.
		u.unwrapped -= int64(1)<<32 - int64(raw)
		anomaly = AnomBackward
	default:
		// Implausibly large forward jump.
		u.unwrapped += int64(raw)
		anomaly = AnomJump
	}
	u.lastTS = ts

	if anomaly == AnomNone && udelta > 1 {
		anomaly = AnomSeqGap // missing records, but the clock itself is intact
	}
	if anomaly == AnomBackward || anomaly == AnomJump {
		return u.isolate(index, seq, ts, arrival, anomaly), nil
	}
	return u.packet(index, uint16(seq), u.curExtSeq(), ts, arrival, anomaly), nil
}

// isolate marks the packet as part of an isolated region. Consecutive
// isolated packets extend one range; a healthy packet later closes it.
func (u *Unwrapper) isolate(index int, seq, ts uint32, arrival time.Time, reason AnomalyKind) *Packet {
	ext := uint64(u.cycles)*65536 + uint64(seq)
	if u.cur == nil {
		u.region++
		u.isolated = true
		u.cur = &Range{Reason: reason, StartRecord: index, EndRecord: index,
			StartExtSeq: ext, EndExtSeq: ext}
	} else {
		u.cur.Reason = reason
		u.cur.EndRecord = index
		u.cur.EndExtSeq = ext
	}
	return u.packet(index, uint16(seq), ext, ts, arrival, reason)
}

func (u *Unwrapper) curExtSeq() uint64 {
	return uint64(u.cycles)*65536 + uint64(u.lastSeq)
}

// packet applies the region transition rules and builds the result.
func (u *Unwrapper) packet(index int, seq uint16, ext uint64, ts uint32, arrival time.Time, a AnomalyKind) *Packet {
	healthy := a == AnomNone || a == AnomWrap || a == AnomSeqGap || a == AnomDuplicate
	if u.isolated && healthy {
		u.ranges = append(u.ranges, *u.cur)
		u.cur = nil
		u.isolated = false
		u.region++
	}
	return &Packet{
		Index: index, Seq: seq, ExtSeq: ext, RTP: ts,
		Unwrapped: u.unwrapped, Arrival: arrival,
		RegionID: u.region, Isolated: u.isolated, Anomaly: a,
	}
}

// Finish closes any isolated region still open at end of stream.
func (u *Unwrapper) Finish() {
	if u.cur != nil {
		u.ranges = append(u.ranges, *u.cur)
		u.cur = nil
	}
}
