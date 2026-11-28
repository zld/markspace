// Package rtp reorders RTP packet records into a per-SSRC media timeline.
//
// Packets arrive out of order, duplicated or missing. The reorderer keeps an
// extension window of sequence numbers ahead of the reader, emits packets in
// strictly increasing sequence order, counts duplicates once, and records the
// numbers that never arrived as losses once the window has passed them. It
// never invents packets: an emitted packet always corresponds to one parsed
// input record.
package rtp

import (
	"fmt"
	"sort"

	pionrtp "github.com/pion/rtp"

	"rtp-rebuild/internal/clock"
	"rtp-rebuild/internal/fault"
)

const (
	// DefaultWindow is the default reorder window in sequence numbers.
	DefaultWindow = 256
	// MaxWindow bounds the window so one SSRC can never hold unbounded state.
	MaxWindow = 2048
	// minRTPHeader is the smallest valid RTP packet.
	minRTPHeader = 12
	// pendingBytesPerPacket estimates reorder bookkeeping per buffered packet.
	pendingBytesPerPacket = 64
)

// Packet is one parsed RTP packet on the reconstructed timeline.
type Packet struct {
	SSRC     uint32
	Seq      uint16
	ExtSeq   int64
	RawTS    uint32
	TS       clock.Tick
	Marker   bool
	PayloadT uint8
	// Payload is the RTP payload, kept so the mux stage can write it as a
	// sample. The 4 MiB input cap bounds the total payload retained.
	Payload  []byte
	InputOff int64
	// TSSuspect marks a packet whose timestamp could not be unwrapped. Its TS
	// is not meaningful for interval analysis.
	TSSuspect bool
}

// Loss is one run of missing sequence numbers. First/Last are the wire values;
// ExtFirst/ExtLast are the same run on the unwrapped timeline.
type Loss struct {
	SSRC     uint32
	First    uint16
	Last     uint16
	ExtFirst int64
	ExtLast  int64
	Count    int
}

// Anomaly is a packet that could not be placed on the timeline.
type Anomaly struct {
	SSRC   uint32
	Seq    uint16
	ExtSeq int64
	Offset int64
	Reason string
}

// Result is the reconstruction of one input record stream for one SSRC.
type Result struct {
	SSRC           uint32
	Packets        []Packet
	Losses         []Loss
	Anomalies      []Anomaly
	Duplicates     int
	OutOfWindow    int
	TimestampWraps int
	// PeakPendingBytes is the high-water mark of buffered reorder state,
	// estimated as fixed bookkeeping per pending packet.
	PeakPendingBytes int
}

// LossesTotal returns the number of missing packets.
func (r *Result) LossesTotal() int {
	total := 0
	for _, l := range r.Losses {
		total += l.Count
	}
	return total
}

// LossRate returns lost/(received+lost), 0 for an empty stream.
func (r *Result) LossRate() float64 {
	lost := r.LossesTotal()
	got := len(r.Packets)
	if got+lost == 0 {
		return 0
	}
	return float64(lost) / float64(got+lost)
}

// Reorderer is a single-pass reorderer for one SSRC.
type Reorderer struct {
	window int

	started bool
	ssrc    uint32
	highest int64 // highest extended sequence number seen
	emitted int64 // next extended sequence number to emit

	// received holds packets awaiting emission; the window bounds how far
	// ahead of the emit cursor it can grow.
	received map[int64]Packet
	// lossRanges remembers declared losses in ascending order so a late
	// arrival is reported as an anomaly instead of a duplicate. Ranges keep
	// this bounded by the number of gaps, not by their size.
	lossRanges []lossRange
	// starting is true until the first window of packets has been observed.
	// The first packet of a capture may itself be out of order, so the start
	// cursor is derived from the lowest buffered packet, not the first seen.
	starting bool

	unwrap clock.Unwrapper
	tsSeen bool
	tsLast uint32

	result Result
}

// NewReorderer builds a reorderer with the given window (clamped to 1..MaxWindow).
func NewReorderer(window int) *Reorderer {
	if window <= 0 {
		window = DefaultWindow
	}
	if window > MaxWindow {
		window = MaxWindow
	}
	return &Reorderer{
		window:   window,
		received: make(map[int64]Packet),
		starting: true,
	}
}

// lossRange is one inclusive run of declared-lost sequence numbers.
type lossRange struct{ from, to int64 }

func (r *Reorderer) lossDeclared(ext int64) bool {
	lo, hi := 0, len(r.lossRanges)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		rg := r.lossRanges[mid]
		switch {
		case ext < rg.from:
			hi = mid - 1
		case ext > rg.to:
			lo = mid + 1
		default:
			return true
		}
	}
	return false
}

// Window reports the effective window size in sequence numbers.
func (r *Reorderer) Window() int { return r.window }

// Push parses one RTP record and places it on the timeline.
//
// A parse failure is returned as a structured error; the packet is skipped and
// the caller may continue with the rest of the stream.
func (r *Reorderer) Push(data []byte, offset int64) error {
	if len(data) < minRTPHeader {
		return fault.At(fault.StageRTP, "parse rtp header", offset,
			fmt.Errorf("%w: %d byte packet", fault.ErrTruncated, len(data)))
	}
	var pkt pionrtp.Packet
	if err := pkt.Unmarshal(data); err != nil {
		return fault.At(fault.StageRTP, "parse rtp packet", offset,
			fmt.Errorf("%w: %v", fault.ErrCorrupt, err))
	}
	if pkt.Version != 2 {
		return fault.At(fault.StageRTP, "parse rtp packet", offset,
			fmt.Errorf("%w: rtp version %d", fault.ErrUnsupported, pkt.Version)).WithSeq(int(pkt.SequenceNumber))
	}
	r.accept(pkt.SSRC, pkt.SequenceNumber, pkt.Timestamp, pkt.Marker, pkt.PayloadType, offset, pkt.Payload)
	return nil
}

// Accept feeds one already-parsed packet. It exists so callers that already
// hold pion packets (tests, the sample generator) do not re-marshal bytes.
func (r *Reorderer) Accept(ssrc uint32, seq uint16, rawTS uint32, marker bool, pt uint8, offset int64) {
	r.accept(ssrc, seq, rawTS, marker, pt, offset, nil)
}

func (r *Reorderer) accept(ssrc uint32, seq uint16, rawTS uint32, marker bool, pt uint8, offset int64, payload []byte) {
	r.result.SSRC = ssrc
	if !r.started {
		r.started = true
		r.ssrc = ssrc
		r.highest = int64(seq)
	} else if ssrc != r.ssrc {
		r.result.Anomalies = append(r.result.Anomalies, Anomaly{
			SSRC: ssrc, Seq: seq, ExtSeq: -1, Offset: offset,
			Reason: fmt.Sprintf("ssrc %d differs from stream ssrc %d", ssrc, r.ssrc),
		})
		return
	}

	ext := r.extend(seq)

	// Already buffered, or already emitted: both are duplicates.
	if _, ok := r.received[ext]; ok {
		r.result.Duplicates++
		return
	}
	if !r.starting && ext < r.emitted {
		if r.lossDeclared(ext) {
			r.result.OutOfWindow++
			r.result.Anomalies = append(r.result.Anomalies, Anomaly{
				SSRC: ssrc, Seq: seq, ExtSeq: ext, Offset: offset,
				Reason: "arrived after its slot was declared lost",
			})
			return
		}
		r.result.Duplicates++
		return
	}

	if ext > r.highest {
		r.highest = ext
	}
	r.received[ext] = Packet{
		SSRC: ssrc, Seq: seq, ExtSeq: ext, RawTS: rawTS,
		Marker: marker, PayloadT: pt, Payload: payload, InputOff: offset,
	}
	if bytes := r.pendingBytes(); bytes > r.result.PeakPendingBytes {
		r.result.PeakPendingBytes = bytes
	}
	if r.starting && len(r.received) >= r.window {
		r.resolveStart()
	}
	r.drain()
}

// pendingBytes estimates buffered memory: fixed per-packet bookkeeping plus
// the payloads still waiting for emission.
func (r *Reorderer) pendingBytes() int {
	bytes := len(r.received) * pendingBytesPerPacket
	for _, p := range r.received {
		bytes += len(p.Payload)
	}
	return bytes
}

// resolveStart fixes the emit cursor at the lowest buffered sequence number
// once enough packets have been observed to trust the ordering.
func (r *Reorderer) resolveStart() {
	if !r.starting {
		return
	}
	lowest, ok := r.lowestBuffered()
	if !ok {
		return
	}
	r.starting = false
	r.emitted = lowest
}

// extend maps a 16 bit sequence number onto the unwrapped timeline using the
// signed difference from the highest number seen so far.
func (r *Reorderer) extend(seq uint16) int64 {
	return r.highest + int64(int16(seq-uint16(r.highest)))
}

// drain emits every consecutively available packet. When the gap ahead of the
// cursor reaches the window size, the missing numbers can no longer be
// recovered and are declared lost.
func (r *Reorderer) drain() {
	if r.starting {
		return
	}
	for {
		if p, ok := r.received[r.emitted]; ok {
			r.emit(p)
			continue
		}
		limit := r.highest - int64(r.window)
		if limit < r.emitted {
			return // window not exhausted; wait for the missing packet
		}
		if next, ok := r.lowestBuffered(); ok && next <= limit {
			// Everything before the next buffered packet is lost.
			r.declareLoss(r.emitted, next-1)
			r.emitted = next
			continue
		}
		r.declareLoss(r.emitted, limit)
		r.emitted = limit + 1
	}
}

// lowestBuffered returns the lowest buffered sequence number at or above the
// emit cursor. Buffered entries are window-bounded, so the scan is too.
func (r *Reorderer) lowestBuffered() (int64, bool) {
	best := int64(-1)
	for ext := range r.received {
		if ext >= r.emitted && (best < 0 || ext < best) {
			best = ext
		}
	}
	return best, best >= 0
}

func (r *Reorderer) emit(p Packet) {
	delete(r.received, p.ExtSeq)
	if r.tsSeen && tsWrapped(r.tsLast, p.RawTS) {
		r.result.TimestampWraps++
	}
	r.tsSeen, r.tsLast = true, p.RawTS
	ts, err := r.unwrap.Unwrap(p.RawTS)
	p.TSSuspect = err != nil
	if err != nil {
		// Overflow is recorded but must not stop the stream: downstream stages
		// exclude the packet from interval checks by its anomaly entry.
		r.result.Anomalies = append(r.result.Anomalies, Anomaly{
			SSRC: p.SSRC, Seq: p.Seq, ExtSeq: p.ExtSeq, Offset: p.InputOff,
			Reason: fmt.Sprintf("timestamp overflow: %v", err),
		})
	}
	p.TS = ts
	r.result.Packets = append(r.result.Packets, p)
	r.emitted = p.ExtSeq + 1
}

// tsWrapped reports whether the transition prev -> ts crossed the 32 bit
// timestamp boundary.
func tsWrapped(prev, ts uint32) bool {
	return ts < prev && int32(ts-prev) > 0
}

func (r *Reorderer) declareLoss(from, to int64) {
	if to < from {
		return
	}
	r.result.Losses = append(r.result.Losses, Loss{
		SSRC:     r.ssrc,
		First:    uint16(from & 0xffff),
		Last:     uint16(to & 0xffff),
		ExtFirst: from,
		ExtLast:  to,
		Count:    int(to - from + 1),
	})
	r.lossRanges = append(r.lossRanges, lossRange{from: from, to: to})
}

// Finish flushes the tail. Buffered packets are emitted in order and every
// remaining hole up to the highest sequence number becomes a loss.
func (r *Reorderer) Finish() *Result {
	if !r.started {
		return &r.result
	}
	if r.starting {
		r.resolveStart()
		r.drain()
	}
	for ext := r.emitted; ext <= r.highest; ext++ {
		if p, ok := r.received[ext]; ok {
			r.emit(p)
			continue
		}
		r.result.Losses = append(r.result.Losses, Loss{
			SSRC: r.ssrc, First: uint16(ext & 0xffff), Last: uint16(ext & 0xffff),
			ExtFirst: ext, ExtLast: ext, Count: 1,
		})
		r.lossRanges = append(r.lossRanges, lossRange{from: ext, to: ext})
	}
	for ext, p := range r.received {
		r.result.Anomalies = append(r.result.Anomalies, Anomaly{
			SSRC: p.SSRC, Seq: p.Seq, ExtSeq: ext, Offset: p.InputOff,
			Reason: "unplaced at end of stream",
		})
	}
	r.mergeLosses()
	return &r.result
}

// mergeLosses collapses adjacent loss runs so the manifest lists maximal gaps.
func (r *Reorderer) mergeLosses() {
	if len(r.result.Losses) < 2 {
		return
	}
	sort.SliceStable(r.result.Losses, func(i, j int) bool {
		return r.result.Losses[i].ExtFirst < r.result.Losses[j].ExtFirst
	})
	merged := r.result.Losses[:1]
	for _, l := range r.result.Losses[1:] {
		last := &merged[len(merged)-1]
		if l.SSRC == last.SSRC && last.ExtLast+1 == l.ExtFirst {
			last.Count += l.Count
			last.ExtLast = l.ExtLast
			last.Last = l.Last
			continue
		}
		merged = append(merged, l)
	}
	r.result.Losses = merged
}
