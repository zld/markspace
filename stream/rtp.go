package stream

import (
	"github.com/pion/rtp"

	"medpipe/timeline"
)

// MaxSeqJump is the largest forward sequence distance accepted as normal
// packet loss. A larger discontinuity is reported as a hard sequence jump.
const MaxSeqJump = 16

// RTPValidator is the stateful per-track checker for transported blocks.
// Sequence numbers and timestamps are 16/32-bit RTP fields, so wrap-around is
// handled with unsigned modular arithmetic rather than plain comparison.
type RTPValidator struct {
	trackID string
	have    bool
	lastSeq uint16
	lastTS  uint32
}

// NewRTPValidator builds a validator bound to one track id for error location.
func NewRTPValidator(trackID string) *RTPValidator {
	return &RTPValidator{trackID: trackID}
}

// Observe applies one parsed packet and returns a located error on duplicate
// sequence, excessive jump or backwards timestamp.
func (v *RTPValidator) Observe(pkt *rtp.Packet) error {
	if !v.have {
		v.have = true
		v.lastSeq = pkt.SequenceNumber
		v.lastTS = pkt.Timestamp
		return nil
	}
	delta := pkt.SequenceNumber - v.lastSeq // modular uint16 subtraction
	switch {
	case delta == 0:
		return timeline.NewError(timeline.ErrCodeDuplicateTimestamp, timeline.StageRTP, v.trackID,
			"duplicate RTP sequence number %d", pkt.SequenceNumber)
	case delta > MaxSeqJump:
		return timeline.NewError(timeline.ErrCodeSeqJump, timeline.StageRTP, v.trackID,
			"RTP sequence jumped %d packets (from %d to %d)",
			delta, v.lastSeq, pkt.SequenceNumber)
	}
	// Timestamps may legitimately stay equal across marker packets, but must
	// never move backwards while the sequence number advanced.
	tsDelta := pkt.Timestamp - v.lastTS // modular uint32 subtraction
	if tsDelta > 1<<31 {
		return timeline.NewError(timeline.ErrCodeTimestampBackwards, timeline.StageRTP, v.trackID,
			"RTP timestamp moved backwards (from %d to %d)", v.lastTS, pkt.Timestamp)
	}
	v.lastSeq = pkt.SequenceNumber
	v.lastTS = pkt.Timestamp
	return nil
}

// CheckPacket unmarshals one raw RTP datagram and validates it.
func (v *RTPValidator) CheckPacket(raw []byte) (*rtp.Packet, error) {
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(raw); err != nil {
		return nil, timeline.NewError(timeline.ErrCodeIllegalBlockLength, timeline.StageRTP, v.trackID,
			"malformed RTP packet: %v", err)
	}
	if err := v.Observe(pkt); err != nil {
		return nil, err
	}
	return pkt, nil
}

// LastSequence returns the most recently accepted sequence number.
func (v *RTPValidator) LastSequence() (uint16, bool) {
	return v.lastSeq, v.have
}
