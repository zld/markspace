package cue

import (
	"fmt"

	"github.com/pion/rtp"

	"continuity/internal/media"
)

// RTPStream expands 16-bit sequence numbers and 32-bit RTP timestamps while
// retaining packet-loss gaps explicitly.
type RTPStream struct {
	TrackID   uint32
	Kind      media.EventKind
	ClockRate uint32
	SourceID  string

	haveSeq    bool
	lastSeqExt int64
	haveTS     bool
	lastTSExt  int64

	Packets     int
	LostPackets int
	LossRanges  []LossRange
}

// LossRange records a sequence hole. The missing packets are not converted to
// media events as if they had arrived.
type LossRange struct {
	Before         uint16 `json:"before"`
	After          uint16 `json:"after"`
	ExtendedBefore int64  `json:"extended_before"`
	ExtendedAfter  int64  `json:"extended_after"`
	MissingCount   int    `json:"missing_count"`
}

// ParsePacket parses one raw RTP packet using github.com/pion/rtp.
func ParsePacket(data []byte, offset int64) (*rtp.Packet, error) {
	var p rtp.Packet
	if err := p.Unmarshal(data); err != nil {
		wrapped := fmt.Errorf("%w: %v", media.ErrInvalidVINT, err)
		return nil, media.NewError(media.StageCue, offset, "rtp", wrapped).
			WithKind("packet_unmarshal")
	}
	return &p, nil
}

// Feed parses and expands one raw packet, returning its event plus an explicit
// loss marker when a sequence hole is detected.
func (s *RTPStream) Feed(data []byte, offset int64) ([]media.Event, error) {
	pkt, err := ParsePacket(data, offset)
	if err != nil {
		return nil, err
	}
	return s.Push(pkt, offset)
}

// Push expands an already parsed RTP packet.
func (s *RTPStream) Push(pkt *rtp.Packet, offset int64) ([]media.Event, error) {
	if s.ClockRate == 0 {
		return nil, media.NewError(media.StageCue, offset, "rtp", media.ErrTimeOverflow).
			WithTrack(s.TrackID).WithKind("zero_clock_rate")
	}
	seqExt := ExpandSequence(s.lastSeqExt, s.haveSeq, pkt.SequenceNumber)
	tsExt := ExpandTimestamp(s.lastTSExt, s.haveTS, pkt.Timestamp)

	var events []media.Event
	if s.haveSeq && seqExt > s.lastSeqExt+1 {
		missing := int(seqExt - s.lastSeqExt - 1)
		s.LostPackets += missing
		s.LossRanges = append(s.LossRanges, LossRange{
			Before:         uint16(pkt.SequenceNumber - uint16(missing) - 1),
			After:          pkt.SequenceNumber,
			ExtendedBefore: s.lastSeqExt,
			ExtendedAfter:  seqExt,
			MissingCount:   missing,
		})
		pts, err := media.ScaleTicks(uint64(tsExt), s.ClockRate)
		if err != nil {
			return nil, media.NewError(media.StageCue, offset, "rtp", err).WithTrack(s.TrackID)
		}
		events = append(events, media.Event{
			TrackID:      s.TrackID,
			Kind:         s.Kind,
			PTSNS:        pts,
			EndNS:        pts,
			SourceID:     s.SourceID,
			Offset:       offset,
			Lost:         true,
			MissingCount: missing,
			Detail:       fmt.Sprintf("missing RTP sequences after ext=%d", s.lastSeqExt),
		})
	}

	ptsNS, err := media.ScaleTicks(uint64(tsExt), s.ClockRate)
	if err != nil {
		return nil, media.NewError(media.StageCue, offset, "rtp", err).WithTrack(s.TrackID)
	}
	s.Packets++
	s.haveSeq = true
	s.lastSeqExt = seqExt
	s.haveTS = true
	s.lastTSExt = tsExt

	events = append(events, media.Event{
		TrackID:        s.TrackID,
		Sequence:       int(seqExt),
		GlobalSequence: s.Packets - 1,
		Kind:           s.Kind,
		PTSNS:          ptsNS,
		SourceID:       s.SourceID,
		Offset:         offset,
		Detail:         fmt.Sprintf("rtp seq=%d marker=%v", pkt.SequenceNumber, pkt.Marker),
	})
	return events, nil
}

// ExpandSequence expands a uint16 sequence near prev into a monotone-ish
// int64. Reordered old packets are allowed; forward distance determines loss.
func ExpandSequence(prev int64, havePrev bool, next uint16) int64 {
	if !havePrev {
		return int64(next)
	}
	return expandWrapped(prev, int64(next), 1<<16)
}

// ExpandTimestamp expands a uint32 RTP timestamp near prev.
func ExpandTimestamp(prev int64, havePrev bool, next uint32) int64 {
	if !havePrev {
		return int64(next)
	}
	return expandWrapped(prev, int64(next), 1<<32)
}

func expandWrapped(prev, low int64, modulus int64) int64 {
	base := prev - (prev % modulus)
	best := base + low
	for _, cand := range []int64{best - modulus, best, best + modulus} {
		if absInt64(cand-(prev+1)) < absInt64(best-(prev+1)) {
			best = cand
		}
	}
	return best
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
