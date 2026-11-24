package mediacore

import (
	"fmt"
	"sort"

	"github.com/pion/rtp"
)

// RTPConfig describes how a single RTP stream is recovered.
type RTPConfig struct {
	TrackID   uint32
	Stream    string
	ClockRate uint32
	// SSRC pins the expected source; zero adopts the first packet's SSRC.
	SSRC uint32
	// Window is the reorder window measured in packets. A hole further than
	// Window packets behind the highest sequence is declared missing; a hole
	// inside the window stays pending and may still be filled by a late
	// packet.
	Window uint32
	// PayloadType, when non-zero, rejects packets from another codec.
	PayloadType uint8
}

// DefaultRTPConfig returns a conservative window for a stream.
func DefaultRTPConfig(trackID uint32, stream string, clockRate uint32) RTPConfig {
	return RTPConfig{TrackID: trackID, Stream: stream, ClockRate: clockRate, Window: 64}
}

// RTPPacket is one recovered packet. Payload bytes and the media timestamp
// are preserved exactly as received; bandwidth pacing never edits them.
type RTPPacket struct {
	TrackID     uint32 `json:"track_id"`
	Stream      string `json:"stream"`
	Seq         uint16 `json:"seq"`
	ExtSeq      uint64 `json:"ext_seq"`
	Timestamp   uint32 `json:"timestamp"`
	Marker      bool   `json:"marker"`
	PayloadType uint8  `json:"payload_type"`
	Size        int    `json:"size"`
	Payload     []byte `json:"-"`
}

// RecoveryStats summarises delivery handling for one stream.
type RecoveryStats struct {
	TrackID    uint32 `json:"track_id"`
	Stream     string `json:"stream"`
	Received   uint64 `json:"received"`
	Unique     uint64 `json:"unique"`
	Duplicates uint64 `json:"duplicates"`
	Reordered  uint64 `json:"reordered"`
	Missing    uint64 `json:"missing"`
	LateFilled uint64 `json:"late_filled"`
	// Discarded counts packets that arrived after their hole had already
	// been declared terminal. They are not inserted, so output never depends
	// on a packet that missed the window.
	Discarded uint64 `json:"discarded"`
	Gaps      []Gap  `json:"gaps"`
}

// RTPStream recovers one SSRC's packets. It never invents media samples: a
// hole either receives its original packet late or becomes a terminal gap.
type RTPStream struct {
	cfg      RTPConfig
	haveBase bool
	highest  uint64
	seen     map[uint64]bool
	pending  map[uint64]int
	terminal map[uint64]bool
	order    []RTPPacket
	gaps     GapLog
	stats    RecoveryStats
}

// NewRTPStream validates cfg and returns an empty stream.
func NewRTPStream(cfg RTPConfig) (*RTPStream, error) {
	if cfg.TrackID == 0 {
		return nil, fail(KindInvalidConfig, "rtp", "track ID must be non-zero")
	}
	if cfg.ClockRate == 0 {
		return nil, fail(KindInvalidConfig, "rtp", "clock rate must be non-zero")
	}
	if cfg.Window == 0 {
		cfg.Window = 64
	}
	return &RTPStream{
		cfg:      cfg,
		seen:     map[uint64]bool{},
		pending:  map[uint64]int{},
		terminal: map[uint64]bool{},
	}, nil
}

// Config returns the stream configuration.
func (s *RTPStream) Config() RTPConfig { return s.cfg }

// Feed accepts one wire packet. Duplicates and reordering are normal input;
// malformed packets are reported as transport errors.
func (s *RTPStream) Feed(raw []byte) error {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		return wrapErr(KindTransport, "rtp", err)
	}
	if s.cfg.SSRC == 0 {
		s.cfg.SSRC = pkt.SSRC
	}
	if pkt.SSRC != s.cfg.SSRC {
		return fail(KindTransport, "rtp", "track %d: unexpected SSRC %d (want %d)", s.cfg.TrackID, pkt.SSRC, s.cfg.SSRC)
	}
	if s.cfg.PayloadType != 0 && pkt.PayloadType != s.cfg.PayloadType {
		return fail(KindTransport, "rtp", "track %d: unexpected payload type %d (want %d)", s.cfg.TrackID, pkt.PayloadType, s.cfg.PayloadType)
	}
	s.stats.Received++

	ext := s.extend(pkt.SequenceNumber)
	if s.seen[ext] {
		s.stats.Duplicates++
		return nil
	}
	if s.terminal[ext] {
		// The hole was already reported missing; inserting the packet now
		// would make results depend on arrival timing after the window.
		s.stats.Discarded++
		return nil
	}

	payload := append([]byte(nil), pkt.Payload...)
	rec := RTPPacket{
		TrackID:     s.cfg.TrackID,
		Stream:      s.cfg.Stream,
		Seq:         pkt.SequenceNumber,
		ExtSeq:      ext,
		Timestamp:   pkt.Timestamp,
		Marker:      pkt.Marker,
		PayloadType: pkt.PayloadType,
		Size:        len(payload),
		Payload:     payload,
	}

	switch {
	case !s.haveBase:
		s.haveBase = true
		s.highest = ext
	case ext > s.highest:
		if ext > s.highest+1 {
			s.openGap(s.highest+1, ext-1)
		}
		s.highest = ext
	case ext < s.highest:
		s.stats.Reordered++
		if idx, ok := s.pending[ext]; ok {
			s.gaps.Gaps[idx].State = GapRecovered
			s.stats.LateFilled++
			delete(s.pending, ext)
		}
	}

	s.seen[ext] = true
	s.stats.Unique++
	s.insertOrdered(rec)
	s.pruneWindow()
	return nil
}

// extend maps a 16-bit sequence onto the extended timeline anchored at the
// highest sequence seen so far, choosing the wrap-adjacent interpretation
// nearest to that anchor.
func (s *RTPStream) extend(seq uint16) uint64 {
	if !s.haveBase {
		return uint64(seq)
	}
	base := s.highest & ^uint64(0xFFFF)
	cand := base | uint64(seq)
	diff := int64(cand) - int64(s.highest)
	if diff < -32768 {
		cand += 0x10000
	} else if diff > 32768 && cand >= 0x10000 {
		cand -= 0x10000
	}
	return cand
}

func (s *RTPStream) openGap(first, last uint64) {
	for seq := first; seq <= last; seq++ {
		if s.seen[seq] || s.terminal[seq] {
			continue
		}
		if _, ok := s.pending[seq]; ok {
			continue
		}
		idx := s.gaps.Add(Gap{
			TrackID: s.cfg.TrackID, Stream: s.cfg.Stream,
			FirstSeq: seq, LastSeq: seq, State: GapPending,
		})
		s.pending[seq] = idx
	}
}

func (s *RTPStream) insertOrdered(rec RTPPacket) {
	pos := sort.Search(len(s.order), func(i int) bool { return s.order[i].ExtSeq >= rec.ExtSeq })
	s.order = append(s.order, RTPPacket{})
	copy(s.order[pos+1:], s.order[pos:])
	s.order[pos] = rec
}

// pruneWindow declares terminal every hole that has fallen more than Window
// packets behind the highest sequence. Pruning is idempotent.
func (s *RTPStream) pruneWindow() {
	if !s.haveBase {
		return
	}
	for seq, idx := range s.pending {
		if s.highest-seq > uint64(s.cfg.Window) {
			s.markMissing(seq, idx)
		}
	}
}

func (s *RTPStream) markMissing(seq uint64, idx int) {
	s.gaps.Gaps[idx].State = GapMissing
	s.terminal[seq] = true
	s.stats.Missing++
	delete(s.pending, seq)
}

// Flush ends the stream. Pending holes become terminal gaps; no synthetic
// sample is ever produced for them.
func (s *RTPStream) Flush() RecoveryStats {
	for seq, idx := range s.pending {
		s.markMissing(seq, idx)
	}
	s.stats.TrackID = s.cfg.TrackID
	s.stats.Stream = s.cfg.Stream
	s.stats.Gaps = s.coalescedGaps()
	s.stats.Unique = uint64(len(s.order))
	return s.stats
}

// Packets returns the accepted packets ordered by extended sequence number.
func (s *RTPStream) Packets() []RTPPacket {
	return append([]RTPPacket(nil), s.order...)
}

// Gaps returns the gap log with adjacent same-state sequences merged into
// ranges, so callers can report "track, sequence range, state" directly.
func (s *RTPStream) Gaps() []Gap { return s.coalescedGaps() }

func (s *RTPStream) coalescedGaps() []Gap {
	out := make([]Gap, 0, len(s.gaps.Gaps))
	for _, g := range s.gaps.Gaps {
		n := len(out)
		if n > 0 {
			prev := &out[n-1]
			if prev.TrackID == g.TrackID && prev.State == g.State && prev.LastSeq+1 == g.FirstSeq {
				prev.LastSeq = g.LastSeq
				continue
			}
		}
		out = append(out, g)
	}
	return out
}

// Describe renders a one-line recovery summary.
func (s *RTPStream) Describe() string {
	return fmt.Sprintf("rtp stream=%s track=%d received=%d unique=%d duplicate=%d reordered=%d missing=%d gaps=%d",
		s.cfg.Stream, s.cfg.TrackID, s.stats.Received, s.stats.Unique, s.stats.Duplicates,
		s.stats.Reordered, s.stats.Missing, len(s.gaps.Gaps))
}
