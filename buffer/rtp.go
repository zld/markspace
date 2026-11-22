package buffer

import (
	"github.com/pion/rtp"

	"mediacore"
)

// seqWindow bounds how many recently seen extended sequence numbers the
// parser remembers for exact duplicate detection.
const seqWindow = 8192

// RTPParser unwraps 16-bit RTP sequence numbers into 64-bit extended
// sequence numbers (RFC 3550 appendix A style) and turns raw packets into
// mediacore events carrying the unpack/receive time.
type RTPParser struct {
	track       mediacore.TrackID
	initialized bool
	maxSeq      uint64 // highest extended sequence number observed
	seen        map[uint64]struct{}

	// Duplicates counts packets whose extended sequence number was already
	// observed. Duplicates are counted but never emitted twice.
	Duplicates int64
	// Wrapped counts forward 16-bit wrap-arounds observed on this stream.
	Wrapped int
}

// NewRTPParser creates a parser bound to one RTP track (one SSRC).
func NewRTPParser(track mediacore.TrackID) *RTPParser {
	return &RTPParser{track: track, seen: make(map[uint64]struct{})}
}

// extend maps a 16-bit sequence number onto the extended 64-bit axis,
// resolving forward/backward wrap-around by shortest signed distance.
func (p *RTPParser) extend(seq uint16) uint64 {
	high := p.maxSeq &^ uint64(0xffff)
	candidate := high | uint64(seq)
	if d := int64(candidate - p.maxSeq); d > (1 << 15) {
		candidate -= 1 << 16 // backwards across the wrap point (reorder)
	} else if d < -(1 << 15) {
		candidate += 1 << 16 // forwards across the wrap point
	}
	return candidate
}

// Parse unmarshals one RTP packet. It returns the resulting event and reports
// duplicate=true when the extended sequence number has been seen before; the
// caller must count duplicates without emitting them again.
func (p *RTPParser) Parse(raw []byte, recv int64) (ev mediacore.Event, duplicate bool, err error) {
	pkt := &rtp.Packet{}
	if err = pkt.Unmarshal(raw); err != nil {
		return mediacore.Event{}, false,
			mediacore.WrapError(mediacore.CatCorrupt, p.track, "rtp.unmarshal", 0, "malformed RTP packet", err)
	}
	if len(pkt.Payload) > mediacore.MaxBlockBytes {
		return mediacore.Event{}, false,
			mediacore.NewError(mediacore.CatLimit, p.track, "rtp.payload", int64(len(pkt.Payload)),
				"RTP payload exceeds MaxBlockBytes")
	}
	if !p.initialized {
		p.maxSeq = uint64(pkt.SequenceNumber)
		p.initialized = true
	}
	ext := p.extend(pkt.SequenceNumber)
	if _, ok := p.seen[ext]; ok {
		p.Duplicates++
		duplicate = true
	} else {
		p.seen[ext] = struct{}{}
	}
	if ext > p.maxSeq {
		if uint16(ext) < uint16(p.maxSeq) {
			p.Wrapped++
		}
		p.maxSeq = ext
	}
	p.prune()
	ev = mediacore.Event{
		Track:   p.track,
		Kind:    mediacore.KindRTP,
		Recv:    recv,
		Seq:     ext,
		RTPTS:   pkt.Timestamp,
		Payload: append([]byte(nil), pkt.Payload...),
	}
	return ev, duplicate, nil
}

// prune keeps the duplicate-detection set bounded to seqWindow entries below
// the highest observed sequence number.
func (p *RTPParser) prune() {
	if len(p.seen) < seqWindow*2 {
		return
	}
	floor := p.maxSeq - seqWindow
	for s := range p.seen {
		if s < floor {
			delete(p.seen, s)
		}
	}
}
