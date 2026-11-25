// Package rtp recovers deduplicated, reordered RTP packet timelines.
package rtp

import (
	"fmt"
	"sort"

	"github.com/pion/rtp"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
)

// Item is one parsed RTP packet with its fixture arrival index.
type Item struct {
	ArrivalIndex int
	ExtSequence  uint64
	Data         []byte
	Packet       *rtp.Packet
}

// Key returns the SSRC+sequence identity.
func (it Item) Key() (uint32, uint16) { return it.Packet.SSRC, it.Packet.SequenceNumber }

// Stats contains acceptance-test counters.
type Stats struct {
	DuplicatesRemoved int `json:"duplicates_removed"`
	ReorderedPackets  int `json:"reordered_packets"`
	MissingPackets    int `json:"missing_packets"`
	LateRejected      int `json:"late_rejected"`
}

// Report is the result of recovering one or more SSRC streams.
type Report struct {
	Packets []Item      `json:"-"`
	Stats   Stats       `json:"stats"`
	Gaps    []clock.Gap `json:"gaps"`
}

type stream struct {
	items    map[uint64]Item
	seen     map[uint64]struct{}
	base     uint64
	expected uint64
	maxExt   uint64
	started  bool
	haveMax  bool
	seqToExt map[uint16]uint64
}

// Reorderer is a bounded per-SSRC jitter/recovery buffer.
type Reorderer struct {
	window  int
	streams map[uint32]*stream
	Stats   Stats
	Gaps    []clock.Gap
}

// NewReorderer creates a reorderer. window limits how long a missing sequence
// is retained before its gap is committed.
func NewReorderer(window int) *Reorderer {
	if window <= 0 {
		window = 64
	}
	return &Reorderer{window: window, streams: make(map[uint32]*stream)}
}

func (r *Reorderer) stream(ssrc uint32) *stream {
	s := r.streams[ssrc]
	if s == nil {
		s = &stream{items: map[uint64]Item{}, seen: map[uint64]struct{}{}, seqToExt: map[uint16]uint64{}}
		r.streams[ssrc] = s
	}
	return s
}

// Push accepts one packet. Packets released as contiguous media are returned.
// A packet arriving after the timeline has advanced past it is classified as
// late and never rewrites that timeline.
func (r *Reorderer) Push(item Item) ([]Item, error) {
	if item.Packet == nil {
		return nil, fault.New(fault.InvalidArgument, "nil RTP packet")
	}
	p := item.Packet
	s := r.stream(p.SSRC)
	ext := unsequence(s.maxExt, s.haveMax, p.SequenceNumber)
	if known, ok := s.seqToExt[p.SequenceNumber]; ok {
		ext = known
	} else {
		s.seqToExt[p.SequenceNumber] = ext
	}

	if _, dup := s.seen[ext]; dup {
		r.Stats.DuplicatesRemoved++
		return nil, nil
	}

	// Count a newly observed packet behind the highest arrival sequence as
	// out-of-order. Duplicates were handled above.
	if s.haveMax && ext < s.maxExt {
		r.Stats.ReorderedPackets++
	}
	if !s.haveMax || ext > s.maxExt {
		s.maxExt = ext
		s.haveMax = true
	}

	// A gap may already have advanced expected beyond this packet.
	if s.started && ext < s.expected {
		r.Stats.LateRejected++
		return nil, fault.New(fault.Conflict, fmt.Sprintf(
			"late RTP packet ssrc=%d seq=%d ext=%d expected=%d",
			p.SSRC, p.SequenceNumber, ext, s.expected))
	}

	s.seen[ext] = struct{}{}
	item.ExtSequence = ext
	s.items[ext] = item
	return r.release(p.SSRC, false)
}

// Flush commits all buffered packets and emits final gap records.
func (r *Reorderer) Flush() ([]Item, error) {
	var out []Item
	for ssrc := range r.streams {
		items, err := r.release(ssrc, true)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	// Map iteration above is not output order; callers should merge/sort by
	// SSRC/track and extended sequence. Sort merged output deterministically.
	sortItems(out)
	r.ReportGaps()
	return out, nil
}

// ReportGaps exposes accumulated gaps.
func (r *Reorderer) ReportGaps() []clock.Gap { return r.Gaps }

func (r *Reorderer) release(ssrc uint32, force bool) ([]Item, error) {
	s := r.streams[ssrc]
	var out []Item

	if !s.started {
		if len(s.items) == 0 {
			return nil, nil
		}
		min := ^uint64(0)
		for ext := range s.items {
			if ext < min {
				min = ext
			}
		}
		// During streaming, wait until the observed span reaches the window.
		// Flush forces recovery of finite fixtures and end-of-stream data.
		if !force && s.maxExt-min < uint64(r.window) {
			return nil, nil
		}
		s.base = min
		s.expected = min
		s.started = true
	}

	for {
		if s.expected > s.maxExt {
			break
		}
		if item, ok := s.items[s.expected]; ok {
			out = append(out, item)
			delete(s.items, s.expected)
			s.expected++
			continue
		}

		// Missing expected sequence. Wait for a late packet unless flushing or
		// the hole is older than the reorder window.
		if !force && s.maxExt < s.expected+uint64(r.window) {
			break
		}

		// Aggregate the consecutive hole into one clear gap record.
		start := s.expected
		end := start
		for {
			next := end + 1
			if _, ok := s.items[next]; ok {
				break
			}
			if force && next > s.maxExt {
				break
			}
			if !force && s.maxExt < next+uint64(r.window) {
				// Do not claim future sequences are permanently missing yet.
				goto done
			}
			end = next
			if end-start >= uint64(r.window*1024) {
				return nil, fault.New(fault.LimitExceeded, "RTP gap too large")
			}
		}
		count := int(end-start) + 1
		r.Stats.MissingPackets += count
		firstSeq := uint16(start)
		lastSeq := uint16(end)
		r.Gaps = append(r.Gaps, clock.Gap{
			SSRC:         ssrc,
			TrackID:      ssrc, // caller remaps SSRC to track if desired
			StartSeq:     uint32(firstSeq),
			EndSeq:       uint32(lastSeq),
			MissingCount: count,
			Reason:       "sequence gap after reorder window",
		})
		s.expected = end + 1
	}
done:
	return out, nil
}

// ReorderAll parses and recovers a complete finite packet set.
func ReorderAll(packets []*rtp.Packet, window int) (*Report, error) {
	r := NewReorderer(window)
	for i, p := range packets {
		if _, err := r.Push(Item{ArrivalIndex: i, Packet: p}); err != nil && !fault.IsCode(err, fault.Conflict) {
			return nil, err
		}
	}
	ordered, err := r.Flush()
	if err != nil {
		return nil, err
	}
	return &Report{Packets: ordered, Stats: r.Stats, Gaps: r.Gaps}, nil
}

func unsequence(prev uint64, havePrev bool, seq uint16) uint64 {
	if !havePrev {
		return uint64(seq)
	}
	base := prev &^ 0xffff
	candidate := base | uint64(seq)
	switch {
	case int64(candidate)-int64(prev) > 32768:
		candidate -= 1 << 16
	case int64(candidate)-int64(prev) < -32768:
		candidate += 1 << 16
	}
	return candidate
}

func sortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		pi, pj := items[i].Packet, items[j].Packet
		if pi.SSRC != pj.SSRC {
			return pi.SSRC < pj.SSRC
		}
		return items[i].ExtSequence < items[j].ExtSequence
	})
}
