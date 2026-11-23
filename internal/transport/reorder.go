// Package transport orders an RTP stream that may arrive out of order or be
// duplicated, and reports gaps instead of inventing packets.
package transport

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"archivecheck/internal/model"
)

// Packet is one accepted RTP packet after sequence unwrapping.
type Packet struct {
	// Extended is the unwrapped 64-bit sequence number; it keeps increasing
	// across the 16-bit wrap.
	Extended uint64
	// Sequence is the original 16-bit sequence number.
	Sequence uint16
	// Timestamp is the raw RTP timestamp.
	Timestamp uint32
	// Payload is the packet payload. It is used for digests only and never
	// treated as a media sample when the sequence number is missing.
	Payload []byte
	// Marker is the RTP marker bit.
	Marker bool
	// Arrival is the 1-based arrival order in the capture.
	Arrival int
}

// Gap is a contiguous run of missing sequence numbers.
type Gap struct {
	// Before and After bracket the gap: the missing numbers are strictly
	// between them.
	Before uint64 `json:"before"`
	After  uint64 `json:"after"`
	// Count is After-Before-1, the number of missing packets.
	Count uint64 `json:"count"`
	// FirstTimestamp and LastTimestamp are the RTP timestamps of the packets
	// on either side, used to map the gap onto media time.
	FirstTimestamp uint32 `json:"first_timestamp"`
	LastTimestamp  uint32 `json:"last_timestamp"`
	// DetectedAt is the arrival index at which the window expired.
	DetectedAt int `json:"detected_at"`
}

// Duplicate records repeated arrivals of one sequence number.
type Duplicate struct {
	// Extended is the unwrapped sequence number.
	Extended uint64 `json:"extended"`
	// Count is the number of repeated arrivals after the first.
	Count int `json:"count"`
	// Identical is true when every repeat carried the same payload digest.
	Identical bool `json:"identical"`
	// ConflictingDigests lists distinct payload digests in first-seen order.
	// More than one entry means a sequence-number collision.
	ConflictingDigests []string `json:"conflicting_digests,omitempty"`
}

// Result is the ordered stream plus everything the reorder stage observed.
type Result struct {
	// Packets are accepted packets ordered by extended sequence number.
	Packets []Packet `json:"-"`
	// Gaps are missing runs ordered by sequence number.
	Gaps []Gap `json:"gaps"`
	// Duplicates are repeated arrivals ordered by sequence number.
	Duplicates []Duplicate `json:"duplicates"`
	// Missing is the total number of missing packets.
	Missing uint64 `json:"missing"`
	// DuplicateCount is the total number of repeated arrivals.
	DuplicateCount int `json:"duplicate_count"`
	// Reordered is the number of packets that arrived below the highest
	// sequence seen so far.
	Reordered int `json:"reordered"`
	// Received is the number of accepted packets.
	Received int `json:"received"`
	// FirstExtended and LastExtended bound the accepted sequence range.
	FirstExtended uint64 `json:"first_extended"`
	LastExtended  uint64 `json:"last_extended"`
}

// Options tunes the reorder window.
type Options struct {
	// Window is how far ahead of the emission cursor the reorderer waits
	// before committing packets or declaring a gap. Zero uses DefaultWindow.
	Window uint64
	// MaxPackets bounds distinct buffered sequence numbers, enforcing the
	// bounded-memory contract. Zero uses DefaultMaxBuffered.
	MaxPackets int
}

// DefaultWindow is the reorder window in packets.
const DefaultWindow uint64 = 8

// DefaultMaxBuffered bounds the reorder buffer.
const DefaultMaxBuffered = 65536

func (o Options) window() uint64 {
	if o.Window == 0 {
		return DefaultWindow
	}
	return o.Window
}

func (o Options) maxPackets() int {
	if o.MaxPackets == 0 {
		return DefaultMaxBuffered
	}
	return o.MaxPackets
}

// Source identifies the stream a Result belongs to.
type Source struct {
	Segment        string
	Representation string
	File           string
}

// Reorder accepts RTP packets in arrival order and produces an ordered stream
// with gaps. It holds at most Window packets beyond the emission cursor and
// never fabricates a packet.
type Reorder struct {
	opts   Options
	source Source
	// cursor is the next extended sequence number to emit.
	cursor uint64
	// started becomes true once the first packet is committed, which happens
	// only after the reorder window has been observed so leading out-of-order
	// arrivals are still ordered.
	started   bool
	haveFirst bool
	maxSeen   uint64
	pending   map[uint64]Packet
	seen      map[uint64]string
	dups      map[uint64]*Duplicate
	arrival   int
	out       []Packet
	gaps      []Gap
	reordered int
	faults    model.FaultList
}

// NewReorder returns a reorderer for one source stream.
func NewReorder(source Source, opts Options) *Reorder {
	return &Reorder{
		opts:    opts,
		source:  source,
		pending: map[uint64]Packet{},
		seen:    map[uint64]string{},
		dups:    map[uint64]*Duplicate{},
	}
}

// PayloadDigest returns a stable short digest of a payload.
func PayloadDigest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:8])
}

func (r *Reorder) fault(code model.Code, sev model.Severity, format string, args ...any) *model.Fault {
	return model.NewFault(code, sev, model.Source{
		Layer: "transport", Segment: r.source.Segment,
		File: r.source.File, Representation: r.source.Representation, Offset: -1,
	}, format, args...)
}

// Push accepts one packet. It returns an error only when a hard bound is hit;
// ordinary duplicates and gaps are recorded in the Result.
func (r *Reorder) Push(p Packet) error {
	r.arrival++
	if len(r.seen) >= r.opts.maxPackets() {
		return r.fault(model.CodeTransportSequence, model.SeverityError,
			"reorder buffer exceeded %d distinct packets", r.opts.maxPackets())
	}
	ext, ok := r.extend(p.Sequence)
	if !ok {
		return r.fault(model.CodeTransportSequence, model.SeverityError,
			"sequence %d jumps outside the unwrap window", p.Sequence)
	}
	digest := PayloadDigest(p.Payload)
	if prev, ok := r.seen[ext]; ok {
		// A retransmission is counted once; it is never written twice.
		d := r.dups[ext]
		if d == nil {
			d = &Duplicate{Extended: ext, Identical: true, ConflictingDigests: []string{prev}}
			r.dups[ext] = d
		}
		d.Count++
		if prev != digest {
			d.Identical = false
			if !containsString(d.ConflictingDigests, digest) {
				d.ConflictingDigests = append(d.ConflictingDigests, digest)
			}
			r.faults.Add(r.fault(model.CodeTransportDuplicate, model.SeverityError,
				"sequence %d repeated with a different payload digest", p.Sequence))
		}
		return nil
	}
	r.seen[ext] = digest
	p.Extended = ext
	p.Arrival = r.arrival
	if !r.haveFirst {
		r.haveFirst = true
		r.maxSeen = ext
	} else if ext < r.maxSeen {
		r.reordered++
	}
	if ext > r.maxSeen {
		r.maxSeen = ext
	}
	if r.started && ext < r.cursor {
		// The cursor advanced past this sequence only after a gap was
		// declared, so this arrival is unrecoverable in the ordered stream.
		return nil
	}
	r.pending[ext] = p
	return r.drain(false)
}

// Close flushes the buffer and declares any trailing gap.
func (r *Reorder) Close() *Result {
	_ = r.drain(true)
	return r.Result()
}

// drain emits contiguous packets and declares a gap only once the reorder
// window has fully elapsed past a hole. Flushing ignores the window.
func (r *Reorder) drain(flush bool) error {
	for {
		low, ok := r.lowestPending()
		if !ok {
			return nil
		}
		if !r.started {
			if !flush && r.maxSeen < low+r.opts.window() {
				return nil
			}
			r.started = true
			r.cursor = low
		}
		for {
			p, ok := r.pending[r.cursor]
			if !ok {
				break
			}
			r.out = append(r.out, p)
			delete(r.pending, r.cursor)
			r.cursor++
		}
		low, ok = r.lowestPending()
		if !ok {
			return nil
		}
		if low < r.cursor {
			delete(r.pending, low)
			continue
		}
		if !flush && r.maxSeen < r.cursor+r.opts.window() {
			return nil
		}
		g := Gap{
			Before:     r.cursor - 1,
			After:      low,
			Count:      low - r.cursor,
			DetectedAt: r.arrival,
		}
		if len(r.out) > 0 {
			g.FirstTimestamp = r.out[len(r.out)-1].Timestamp
		}
		g.LastTimestamp = r.pending[low].Timestamp
		r.gaps = append(r.gaps, g)
		r.cursor = low
	}
}

func (r *Reorder) lowestPending() (uint64, bool) {
	var best uint64
	found := false
	for ext := range r.pending {
		if !found || ext < best {
			best, found = ext, true
		}
	}
	return best, found
}

// extend unwraps a 16-bit sequence number into the 64-bit space relative to
// the highest sequence seen so far, so out-of-order and wrapped arrivals both
// land on the correct absolute number.
func (r *Reorder) extend(seq uint16) (uint64, bool) {
	if !r.haveFirst {
		return uint64(seq), true
	}
	anchor := r.maxSeen
	delta := int64(seq) - int64(anchor&0xffff)
	switch {
	case delta < -(1 << 15):
		delta += 1 << 16
	case delta > (1<<15)-1:
		delta -= 1 << 16
	}
	ext := int64(anchor) + delta
	if ext < 0 {
		return 0, false
	}
	return uint64(ext), true
}

// Result snapshots the reorderer.
func (r *Reorder) Result() *Result {
	res := &Result{
		Packets:   append([]Packet(nil), r.out...),
		Gaps:      append([]Gap(nil), r.gaps...),
		Reordered: r.reordered,
		Received:  len(r.out),
	}
	for _, d := range r.dups {
		res.Duplicates = append(res.Duplicates, *d)
		res.DuplicateCount += d.Count
	}
	sort.Slice(res.Duplicates, func(i, j int) bool {
		return res.Duplicates[i].Extended < res.Duplicates[j].Extended
	})
	for _, g := range res.Gaps {
		res.Missing += g.Count
	}
	if len(r.out) > 0 {
		res.FirstExtended = r.out[0].Extended
		res.LastExtended = r.out[len(r.out)-1].Extended
	}
	return res
}

// Faults returns payload-collision faults seen while reordering.
func (r *Reorder) Faults() []*model.Fault { return r.faults.All() }

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
