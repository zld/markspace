package stream

import (
	"sort"

	"github.com/pion/rtp"

	"mediaarchive/internal/diag"
)

// Packet is the subset of an RTP packet needed for continuity analysis. It is
// decoded with pion/rtp so header parsing matches a real receiver.
type Packet struct {
	Seq       uint16
	Timestamp uint32
	SSRC      uint32
	Payload   uint8
	Marker    bool
	// Offset is the source element index or byte offset of this packet.
	Offset int64
	// ArrivalMs is the optional capture arrival time.
	ArrivalMs  float64
	HasArrival bool
}

// FromRTP converts a pion packet. rawIdx is the packet's position in input.
func FromRTP(p *rtp.Packet, rawIdx int64) Packet {
	return Packet{
		Seq: p.SequenceNumber, Timestamp: p.Timestamp, SSRC: p.SSRC,
		Payload: p.PayloadType, Marker: p.Marker, Offset: rawIdx,
	}
}

// RTPOptions bounds analyzer memory and tunes jump detection.
type RTPOptions struct {
	ClockRate int    // media ticks per second, e.g. 90000
	MaxFaults int    // hard cap on retained faults (0 => 4096)
	DupWindow int    // reorder/duplicate lookback in sequence numbers
	JumpTicks uint32 // forward timestamp step classified as a burst
}

const seqMod = 1 << 16
const tsMod = 1 << 32

// RTPAnalysis is the deterministic summary of one RTP stream. All state is
// bounded: the fault list, delivered window and hole window have fixed
// capacity regardless of packet count, so a million-packet stream uses the
// same memory as a thousand-packet one.
type RTPAnalysis struct {
	Packets        int64
	SSRCs          map[uint32]int64
	FirstSeq       uint16
	LastSeq        uint16
	OutOfOrder     int64
	RecoveredLate  int64
	Duplicates     int64
	Missing        int64
	MaxGap         int64
	TimestampJumps int64
	Faults         []*diag.Fault
	Suppressed     int64
	UnwrappedSpan  int64
	ClockRate      int

	opts    RTPOptions
	started bool
	highest int64 // unwrapped highest sequence delivered
	seqBase int64 // unwrapped value of the first sequence
	seen    map[int64]bool
	holes   map[int64]bool
	ring    []int64 // FIFO of delivered sequence numbers for eviction

	tsInit bool
	lastTS int64 // unwrapped leading-edge timestamp
}

// NewRTPAnalysis prepares a streaming analyzer. The result is not safe for
// concurrent use; the pipeline gives each worker its own.
func NewRTPAnalysis(opts RTPOptions) *RTPAnalysis {
	if opts.MaxFaults <= 0 {
		opts.MaxFaults = 4096
	}
	if opts.DupWindow <= 0 {
		opts.DupWindow = 512
	}
	if opts.ClockRate <= 0 {
		opts.ClockRate = 90000
	}
	if opts.JumpTicks == 0 {
		opts.JumpTicks = uint32(opts.ClockRate * 2)
	}
	return &RTPAnalysis{
		SSRCs:     map[uint32]int64{},
		ClockRate: opts.ClockRate,
		opts:      opts,
		seen:      make(map[int64]bool, opts.DupWindow),
		holes:     make(map[int64]bool, opts.DupWindow),
		ring:      make([]int64, 0, opts.DupWindow),
	}
}

// Push consumes one packet. Faults are recorded but never stop the scan:
// recovery needs the full timeline and strict mode inspects Faults later.
func (a *RTPAnalysis) Push(p Packet) {
	a.Packets++
	a.SSRCs[p.SSRC]++
	if a.Packets == 1 {
		seq := int64(p.Seq)
		a.FirstSeq, a.LastSeq = p.Seq, p.Seq
		a.seqBase, a.highest = seq, seq
		a.started = true
		a.markSeen(seq)
		a.onTimestamp(p)
		return
	}
	a.LastSeq = p.Seq
	seq := a.unwrapSeq(int64(p.Seq))

	switch {
	case a.seen[seq]:
		a.Duplicates++
		a.addFault(diag.New(diag.CodeRTPDuplicate, p.Offset, "duplicate sequence %d", p.Seq))
		return
	case a.holes[seq]:
		// A late packet that fills a hole: out of order, but delivered.
		delete(a.holes, seq)
		a.Missing--
		a.RecoveredLate++
		a.OutOfOrder++
		a.markSeen(seq)
		a.addFault(diag.New(diag.CodeRTPSegmentGap, p.Offset,
			"late arrival fills hole at seq %d", p.Seq))
		return
	case seq < a.highest:
		a.OutOfOrder++
		a.markSeen(seq)
		a.addFault(diag.New(diag.CodeRTPSegmentGap, p.Offset,
			"out-of-order sequence %d below highest %d", p.Seq, uint16(a.highest)))
		return
	}

	if gap := seq - a.highest - 1; gap > 0 {
		a.Missing += gap
		if gap > a.MaxGap {
			a.MaxGap = gap
		}
		for s := a.highest + 1; s < seq; s++ {
			a.rememberHole(s)
		}
		a.addFault(diag.New(diag.CodeRTPSegmentGap, p.Offset,
			"sequence gap of %d packets after seq %d", gap, uint16(a.highest)))
	}
	a.highest = seq
	a.markSeen(seq)
	a.onTimestamp(p)
}

// unwrapSeq folds a 16-bit sequence number into the neighbourhood of the
// highest sequence seen so far, so a wrap is never mistaken for a rollback.
func (a *RTPAnalysis) unwrapSeq(v int64) int64 {
	base := a.highest
	cand := base - base%seqMod + v
	if cand < base-seqMod/2 {
		cand += seqMod
	} else if cand >= base+seqMod/2 {
		cand -= seqMod
	}
	return cand
}

// markSeen records delivery; eviction keeps memory bounded by DupWindow.
func (a *RTPAnalysis) markSeen(seq int64) {
	a.seen[seq] = true
	a.ring = append(a.ring, seq)
	for len(a.ring) > a.opts.DupWindow {
		old := a.ring[0]
		a.ring = a.ring[1:]
		delete(a.seen, old)
		delete(a.holes, old)
	}
}

func (a *RTPAnalysis) rememberHole(seq int64) {
	if len(a.holes) >= a.opts.DupWindow {
		return
	}
	a.holes[seq] = true
	a.ring = append(a.ring, -seq-1) // negative ids mark holes in the FIFO
	if len(a.ring) > a.opts.DupWindow {
		old := a.ring[0]
		a.ring = a.ring[1:]
		if old < 0 {
			delete(a.holes, -old-1)
		} else {
			delete(a.seen, old)
		}
	}
}

func (a *RTPAnalysis) addFault(f *diag.Fault) {
	if len(a.Faults) >= a.opts.MaxFaults {
		a.Suppressed++
		return
	}
	a.Faults = append(a.Faults, f)
}

// onTimestamp tracks the leading-edge timestamp. Reordered packets carry
// earlier timestamps by design, so only the highest sequence updates the
// clock; this avoids false rollback faults for ordinary jitter.
func (a *RTPAnalysis) onTimestamp(p Packet) {
	raw := int64(p.Timestamp)
	if !a.tsInit {
		a.tsInit = true
		a.lastTS = raw
		return
	}
	step := raw - a.lastTS
	unwrapped := step
	if step < 0 && -step > tsMod/2 {
		unwrapped = step + tsMod // genuine 32-bit wrap
	} else if step < 0 {
		a.addFault(diag.New(diag.CodeTimestampRollback, p.Offset,
			"rtp timestamp rolled back by %d ticks", -step))
		a.lastTS = raw
		return
	}
	if unwrapped > int64(a.opts.JumpTicks) {
		a.TimestampJumps++
		a.addFault(diag.New(diag.CodeRTPBurst, p.Offset,
			"rtp timestamp jumped forward %d ticks (%.1f ms)",
			unwrapped, float64(unwrapped)*1000/float64(a.ClockRate)))
	}
	a.UnwrappedSpan += unwrapped
	a.lastTS = raw
}

// Finish returns the analysis after folding duration information.
func (a *RTPAnalysis) Finish() *RTPAnalysis {
	sort.SliceStable(a.Faults, func(i, j int) bool { return a.Faults[i].Offset < a.Faults[j].Offset })
	return a
}

// DurationMs derives stream duration from the unwrapped timestamp span.
func (a *RTPAnalysis) DurationMs() float64 {
	if a.ClockRate <= 0 {
		return 0
	}
	return float64(a.UnwrappedSpan) * 1000 / float64(a.ClockRate)
}

// AnalyzePackets is the batch convenience wrapper.
func AnalyzePackets(packets []Packet, opts RTPOptions) *RTPAnalysis {
	a := NewRTPAnalysis(opts)
	for _, p := range packets {
		a.Push(p)
	}
	return a.Finish()
}

// Unwrap32 folds a raw 32-bit timestamp sequence, exposing the helper used by
// the alignment stage and tests.
func Unwrap32(raw []uint32) []int64 {
	out := make([]int64, 0, len(raw))
	var high, last int64
	for i, v := range raw {
		cur := int64(v)
		if i > 0 && cur < last && last-cur > tsMod/2 {
			high += tsMod
		}
		out = append(out, high+cur)
		last = cur
	}
	return out
}
