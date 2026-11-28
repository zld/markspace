// Package rtp reorders RTP packets into monotonic sequence order per SSRC.
// Sequence numbers are extended to 64 bits with the wrap logic from
// internal/clock, so 16-bit rollover never resets the media clock. Duplicate
// packets never advance the clock; a duplicate carrying different bytes is a
// hard conflict. Packets arriving outside the reorder window become
// locatable gaps rather than silent loss.
package rtp

import (
	"bytes"
	"crypto/sha256"
	"fmt"

	"github.com/pion/rtp"

	"mediacheck/internal/clock"
	merr "mediacheck/internal/errors"
)

// OrderedPacket is one packet after sequence extension.
type OrderedPacket struct {
	SSRC        uint32
	ExtSequence uint64 // unwrapped 16-bit sequence number
	Sequence    uint16 // original on-wire sequence number
	Timestamp   uint32 // original RTP timestamp (never rewritten)
	PayloadHash [32]byte
	Payload     []byte
	Marker      bool
	PayloadType uint8
	Recovered   bool // true when emitted after a gap had been observed
}

// Gap records a missing sequence range on one SSRC.
type Gap struct {
	SSRC    uint32
	FromExt uint64
	ToExt   uint64 // inclusive
	Count   int
}

// stream is the per-SSRC reorder state.
type stream struct {
	ssrc        uint32
	initialized bool
	nextOut     uint64 // next extended sequence expected for output
	maxSeen     uint64 // highest extended sequence observed
	window      map[uint64]*rtp.Packet
	hashes      map[uint64][32]byte
	gaps        []Gap
	recovered   int
}

// ReorderBuffer keeps per-SSRC streams behind one shared window size.
type ReorderBuffer struct {
	windowSize uint64
	streams    map[uint32]*stream
}

// NewReorderBuffer creates a buffer. windowSize is the maximum out-of-order
// distance tolerated before missing sequences are finalized as gaps.
func NewReorderBuffer(windowSize int) *ReorderBuffer {
	if windowSize <= 0 {
		windowSize = 64
	}
	return &ReorderBuffer{
		windowSize: uint64(windowSize),
		streams:    map[uint32]*stream{},
	}
}

func (b *ReorderBuffer) get(ssrc uint32) *stream {
	s, ok := b.streams[ssrc]
	if !ok {
		s = &stream{
			ssrc:   ssrc,
			window: map[uint64]*rtp.Packet{},
			hashes: map[uint64][32]byte{},
		}
		b.streams[ssrc] = s
	}
	return s
}

// Push ingests one raw RTP datagram. It returns a stable conflict error when
// the same SSRC+sequence arrives with different content. A benign duplicate
// returns (nil, nil) and does not advance the clock.
func (b *ReorderBuffer) Push(raw []byte) error {
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(raw); err != nil {
		return merr.New(merr.CodeInvalidBox, "invalid RTP datagram").
			WithState(merr.StateFatal).Wrap(err)
	}
	s := b.get(pkt.SSRC)
	ext := extendSeq(s, pkt.SequenceNumber)
	hash := sha256.Sum256(pkt.Payload)

	if prior, dup := s.hashes[ext]; dup {
		if !bytes.Equal(prior[:], hash[:]) {
			return merr.New(merr.CodeRTPConflict,
				fmt.Sprintf("ssrc=%d seq=%d arrived with conflicting payload",
					pkt.SSRC, pkt.SequenceNumber)).
				AtTick(int64(pkt.Timestamp), rtpTimescale).
				WithState(merr.StateFatal)
		}
		return nil // identical duplicate: drop, no clock movement
	}

	s.hashes[ext] = hash
	s.window[ext] = pkt
	if !s.initialized {
		// The first observed sequence anchors the output clock. Subsequent
		// sequences are extended relative to the highest value seen.
		s.initialized = true
		s.nextOut = ext
		s.maxSeen = ext
	} else if ext > s.maxSeen {
		s.maxSeen = ext
	}
	return nil
}

// rtpTimescale is a nominal 90kHz label for error timestamps; the raw value is
// always preserved regardless.
const rtpTimescale = 90000

// Drain emits every in-order packet that can currently be released, across
// all SSRCs. When an SSRC falls windowSize behind the highest observed
// sequence, its missing range is finalized as a Gap and output resumes from
// the next packet actually present (independent keyframe recovery).
func (b *ReorderBuffer) Drain() []OrderedPacket {
	var out []OrderedPacket
	for _, s := range b.streams {
		for s.nextOut <= s.maxSeen {
			if pkt, ok := s.window[s.nextOut]; ok {
				recovered := len(s.gaps) > 0 && s.nextOut > s.gaps[len(s.gaps)-1].ToExt
				if recovered {
					s.recovered++
				}
				op := toOrdered(s.ssrc, pkt, s.hashes[s.nextOut], recovered)
				op.ExtSequence = s.nextOut
				out = append(out, op)
				delete(s.window, s.nextOut)
				s.nextOut++
				continue
			}
			// nextOut is genuinely missing. Hold it while it could still
			// arrive inside the reorder window; past the window, finalize the
			// hole up to the next packet actually buffered.
			if s.maxSeen < s.nextOut+b.windowSize {
				break
			}
			nextPresent := s.firstBufferedAtOrAfter(s.nextOut)
			end := min64(nextPresent-1, s.maxSeen-b.windowSize)
			if end < s.nextOut {
				break
			}
			s.gaps = append(s.gaps, Gap{
				SSRC: s.ssrc, FromExt: s.nextOut, ToExt: end,
				Count: int(end - s.nextOut + 1),
			})
			s.nextOut = end + 1
		}
	}
	return out
}

func (s *stream) firstBufferedAtOrAfter(ext uint64) uint64 {
	first := s.maxSeen + 1
	for k := range s.window {
		if k >= ext && k < first {
			first = k
		}
	}
	return first
}

// Flush releases all remaining buffered packets, finalizing any trailing gap.
func (b *ReorderBuffer) Flush() []OrderedPacket {
	for _, s := range b.streams {
		// Force every pending hole to be declared.
		for s.nextOut <= s.maxSeen {
			if _, ok := s.window[s.nextOut]; ok {
				break
			}
			nextPresent := s.firstBufferedAtOrAfter(s.nextOut)
			end := nextPresent - 1
			if end < s.nextOut {
				end = s.maxSeen
			}
			s.gaps = append(s.gaps, Gap{
				SSRC: s.ssrc, FromExt: s.nextOut, ToExt: end,
				Count: int(end - s.nextOut + 1),
			})
			s.nextOut = end + 1
		}
	}
	return b.Drain()
}

// Gaps returns the finalized gaps for one SSRC, or all SSRCs when ssrc == 0.
func (b *ReorderBuffer) Gaps(ssrc uint32) []Gap {
	var out []Gap
	for id, s := range b.streams {
		if ssrc != 0 && id != ssrc {
			continue
		}
		out = append(out, s.gaps...)
	}
	return out
}

// RecoveredCount reports how many packets were emitted after a gap.
func (b *ReorderBuffer) RecoveredCount(ssrc uint32) int {
	if s, ok := b.streams[ssrc]; ok {
		return s.recovered
	}
	return 0
}

func extendSeq(s *stream, seq uint16) uint64 {
	if !s.initialized {
		return uint64(seq)
	}
	return clock.Wrap16(s.maxSeen, seq)
}

func toOrdered(ssrc uint32, p *rtp.Packet, hash [32]byte, recovered bool) OrderedPacket {
	return OrderedPacket{
		SSRC: ssrc, ExtSequence: 0, // filled by caller context where needed
		Sequence: p.SequenceNumber, Timestamp: p.Timestamp,
		PayloadHash: hash, Payload: append([]byte(nil), p.Payload...),
		Marker: p.Marker, PayloadType: p.PayloadType, Recovered: recovered,
	}
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// PacketFor builds a minimal marshalled RTP packet with deterministic payload
// for the given SSRC and sequence number. It is shared by pipeline fixtures
// and tests so synthetic traffic never depends on external captures.
func PacketFor(ssrc uint32, seq uint16) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      uint32(seq) * 160,
			SSRC:           ssrc,
			Marker:         seq%16 == 15,
		},
		Payload: []byte{byte(seq >> 8), byte(seq), 0xa5, 0x5a},
	}
}
