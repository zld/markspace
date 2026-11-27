package rtp

import (
	"sort"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
)

// Segment is a contiguous run of delivered packets in extended sequence order.
type Segment struct {
	Packets      []*Packet `json:"-"`
	From         uint64    `json:"from"`
	To           uint64    `json:"to"`
	MissingAfter uint64    `json:"missing_after"`
}

// Bytes concatenates the payloads of a segment.
func (s Segment) Bytes() []byte {
	n := 0
	for _, p := range s.Packets {
		n += len(p.Payload)
	}
	out := make([]byte, 0, n)
	for _, p := range s.Packets {
		out = append(out, p.Payload...)
	}
	return out
}

// Recovery is the outcome of recovering a packet stream.
type Recovery struct {
	Segments  []Segment      `json:"segments"`
	Packets   int            `json:"packets"`
	Recovered int            `json:"recovered"`
	Missing   int            `json:"missing"`
	Faults    []*fault.Fault `json:"faults,omitempty"`
}

// Bytes concatenates all delivered segments in order.
func (r *Recovery) Bytes() []byte {
	var out []byte
	for _, s := range r.Segments {
		out = append(out, s.Bytes()...)
	}
	return out
}

// Recoverer wraps a Buffer and groups its output into contiguous segments.
// Segments after an unrecoverable gap are still returned so callers can keep
// decoding valid data.
type Recoverer struct {
	buf    *Buffer
	cfg    Config
	report Recovery
	// open is the segment currently being extended.
	open *Segment
}

// NewRecoverer builds a recoverer. A zero-value Config resolves to defaults.
func NewRecoverer(cfg Config) (*Recoverer, error) {
	if cfg == (Config{}) {
		cfg = DefaultConfig()
	}
	buf, err := NewBuffer(cfg)
	if err != nil {
		return nil, err
	}
	return &Recoverer{buf: buf, cfg: cfg}, nil
}

// Push feeds one packet and returns the contiguous segments that became
// deliverable, together with any unrecoverable-gap faults.
func (r *Recoverer) Push(raw []byte, arrival clock.Time) (*Recovery, error) {
	res, err := r.buf.Push(raw, arrival)
	if err != nil {
		return nil, err
	}
	return r.absorb(res), nil
}

// Flush ends the stream and returns the complete recovery report.
func (r *Recoverer) Flush() (*Recovery, error) {
	res, err := r.buf.Close()
	if err != nil {
		return nil, err
	}
	r.absorb(res)
	r.closeSegment()
	stats := r.buf.Stats()
	r.report.Packets = stats.Delivered
	r.report.Recovered = stats.Recovered
	r.report.Missing = stats.Unrecoverable
	return &r.report, nil
}

// absorb groups newly delivered packets into contiguous runs. A run break
// means the buffer skipped an unrecoverable gap.
func (r *Recoverer) absorb(res Result) *Recovery {
	out := &Recovery{}
	for _, f := range res.Faults {
		r.report.Faults = append(r.report.Faults, f)
		out.Faults = append(out.Faults, f)
	}
	for _, p := range res.Delivered {
		if r.open == nil || p.ExtSeq != r.open.To+1 {
			r.closeSegment()
			r.open = &Segment{From: p.ExtSeq, To: p.ExtSeq}
		}
		r.open.Packets = append(r.open.Packets, p)
		r.open.To = p.ExtSeq
	}
	// Expose a lightweight copy of the newly delivered runs to the caller.
	for _, seg := range r.runsFrom(res.Delivered) {
		out.Segments = append(out.Segments, seg)
	}
	return out
}

// runsFrom splits a delivered batch into contiguous runs. The runs are copies
// of the packets slice headers so the caller can inspect boundaries.
func (r *Recoverer) runsFrom(delivered []*Packet) []Segment {
	var runs []Segment
	for _, p := range delivered {
		n := len(runs)
		if n == 0 || p.ExtSeq != runs[n-1].To+1 {
			runs = append(runs, Segment{From: p.ExtSeq, To: p.ExtSeq, Packets: []*Packet{p}})
			continue
		}
		runs[n-1].Packets = append(runs[n-1].Packets, p)
		runs[n-1].To = p.ExtSeq
	}
	return runs
}

func (r *Recoverer) closeSegment() {
	if r.open == nil {
		return
	}
	r.report.Segments = append(r.report.Segments, *r.open)
	r.open = nil
}

// Stats returns the underlying buffer counters.
func (r *Recoverer) Stats() Stats { return r.buf.Stats() }

// Config returns the resolved configuration.
func (r *Recoverer) Config() Config { return r.cfg }

// OrderByExtSeq sorts packets by extended sequence number. It is used by the
// demo packetiser to interleave duplication and reordering deterministically.
func OrderByExtSeq(packets []*Packet) {
	sort.SliceStable(packets, func(i, j int) bool { return packets[i].ExtSeq < packets[j].ExtSeq })
}
