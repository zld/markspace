// Package rtp repairs SSRC-specific extended RTP sequence numbers and emits
// deterministic in-order packets plus explicit sequence gaps.
package rtp

import (
	"github.com/pion/rtp"
)

// Captured is one framed RTP record and its local arrival timestamp.
type Captured struct {
	ArrivalNS int64
	Ext       uint64
	Packet    rtp.Packet
}

// Gap is an inclusive extended-sequence range that remained missing after
// the reorder window closed or the stream was flushed.
type Gap struct {
	SSRC     uint32 `json:"ssrc"`
	StartExt uint64 `json:"start_ext"`
	EndExt   uint64 `json:"end_ext"`
	Missing  int    `json:"missing"`
	StartNS  int64  `json:"start_ns,omitempty"`
	EndNS    int64  `json:"end_ns,omitempty"`
	Mapped   bool   `json:"mapped"`
	MapError string `json:"map_error,omitempty"`
	StartTS  uint32 `json:"start_ts,omitempty"`
	EndTS    uint32 `json:"end_ts,omitempty"`
}

// Repaired is the deterministic repair result for all SSRCs.
type Repaired struct {
	Ordered    []Captured `json:"-"`
	Gaps       []Gap      `json:"gaps"`
	Duplicates int        `json:"duplicate_packets"`
	Missing    int        `json:"missing_packets"`
	Records    int        `json:"records"`
}

type ssrcState struct {
	initialized bool
	next        uint64
	maxExt      uint64
	buf         map[uint64]Captured
	seen        map[uint64]struct{}
	gaps        []Gap
	openGap     *Gap
}

// Repairer keeps independent reorder windows and duplicate sets per SSRC.
type Repairer struct {
	Window  uint64
	states  map[uint32]*ssrcState
	out     []Captured
	dups    int
	records int
}

// NewRepairer creates a repairer with the given sequence reorder window.
func NewRepairer(window uint64) *Repairer {
	if window == 0 {
		window = 64
	}
	return &Repairer{Window: window, states: make(map[uint32]*ssrcState)}
}

// Add ingests one captured RTP packet and returns any packets now ready in
// extended sequence order.
func (r *Repairer) Add(c Captured) []Captured {
	ssrc := c.Packet.SSRC
	st := r.states[ssrc]
	if st == nil {
		st = &ssrcState{buf: make(map[uint64]Captured), seen: make(map[uint64]struct{})}
		r.states[ssrc] = st
	}
	r.records++
	ext := st.extend(c.Packet.SequenceNumber)
	c.Ext = ext
	if _, ok := st.seen[ext]; ok {
		r.dups++
		return nil
	}
	st.seen[ext] = struct{}{}
	st.buf[ext] = c
	if !st.initialized {
		st.initialized = true
		st.next = ext
		st.maxExt = ext
	} else if ext > st.maxExt {
		st.maxExt = ext
	}
	before := len(r.out)
	r.release(ssrc, st, false)
	return r.out[before:]
}

// Flush closes every window, records all remaining missing sequences and
// returns the final globally sequence-ordered output. Input traces are
// expected to contain one main SSRC; with several SSRCs output is ordered by
// SSRC then sequence for deterministic review.
func (r *Repairer) Flush() Repaired {
	for ssrc, st := range r.states {
		r.release(ssrc, st, true)
	}
	// r.out was appended in SSRC first-seen/window readiness order. Sort a
	// copy by SSRC and extended sequence while retaining arrival metadata.
	out := append([]Captured(nil), r.out...)
	sortCaptured(out)
	var gaps []Gap
	missing := 0
	for _, st := range r.states {
		gaps = append(gaps, st.gaps...)
		for _, g := range st.gaps {
			missing += g.Missing
		}
	}
	sortGaps(gaps)
	return Repaired{Ordered: out, Gaps: gaps, Duplicates: r.dups, Missing: missing, Records: r.records}
}

func (st *ssrcState) extend(seq uint16) uint64 {
	if !st.initialized {
		return uint64(seq)
	}
	base := st.maxExt &^ 0xffff
	cand := base | uint64(seq)
	diff := int64(cand) - int64(st.maxExt)
	if diff > 32768 {
		cand -= 1 << 16
	} else if diff < -32768 {
		cand += 1 << 16
	}
	return cand
}

func (r *Repairer) release(ssrc uint32, st *ssrcState, flush bool) {
	for st.next <= st.maxExt {
		if c, ok := st.buf[st.next]; ok {
			if st.openGap != nil {
				st.closeGap()
			}
			r.out = append(r.out, c)
			delete(st.buf, st.next)
			st.next++
			continue
		}
		if !flush && st.maxExt-st.next < r.Window {
			return
		}
		// This sequence is now permanently missing. Coalesce consecutive
		// missing values into one reviewable gap.
		if st.openGap == nil {
			st.openGap = &Gap{SSRC: ssrc, StartExt: st.next, EndExt: st.next, Missing: 1}
		} else {
			st.openGap.EndExt = st.next
			st.openGap.Missing++
		}
		st.next++
	}
	if st.openGap != nil {
		st.closeGap()
	}
}

func (st *ssrcState) closeGap() {
	if st.openGap == nil {
		return
	}
	st.gaps = append(st.gaps, *st.openGap)
	st.openGap = nil
}
