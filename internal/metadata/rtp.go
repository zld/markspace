package metadata

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"

	"replaypipe/internal/timebase"
)

// RTPPacket is one parsed side-band packet. ArrivalUS is the capture wall
// clock in microseconds; it is only used to reconstruct timelines when the
// sender's clock rate is unknown.
type RTPPacket struct {
	Header    rtp.Header
	Payload   []byte
	ArrivalUS int64
	Offset    int64
}

// RTPSession is the result of parsing an RTP side-band capture: ordered
// packets plus the clock-rate/timebase used for timestamp mapping.
type RTPSession struct {
	Packets   []RTPPacket
	ClockRate uint32
	SSRC      uint32
	Base      timebase.Base
	Events    []Event
}

// ParseRTP decodes an RTP side-band capture. The capture format is a sequence
// of length-prefixed packets: a 4-byte big-endian payload length followed by
// the raw RTP packet (RFC 3550 header, optional extensions, no SRTP). A
// truncated prefix or body is BOX_TRUNCATED. Out-of-order sequence numbers are
// reordered, a duplicate sequence number is CORRUPT_FIELD, and packet
// timestamps that regress against the reordered stream are TIME_REGRESSION.
func ParseRTP(data []byte, clockRate uint32, opts ParseOptions) (*RTPSession, *timebase.Error) {
	if clockRate == 0 {
		return nil, timebase.Newf(timebase.CodeCorruptField, opts.TrackID, opts.BaseOffset,
			"rtp clock rate must be positive")
	}
	base, err := timebase.FromTimescale(clockRate)
	if err != nil {
		return nil, timebase.Fail(timebase.CodeTimeOverflow, opts.TrackID, opts.BaseOffset).Wrap(err, "rtp clock")
	}
	sess := &RTPSession{ClockRate: clockRate, Base: base}
	off := 0
	seen := map[uint16]bool{}
	for off < len(data) {
		if off+4 > len(data) {
			return nil, timebase.Newf(timebase.CodeBoxTruncated, opts.TrackID, opts.BaseOffset+int64(off),
				"rtp length prefix truncated, %d bytes remain", len(data)-off)
		}
		n := int(binary.BigEndian.Uint32(data[off : off+4]))
		off += 4
		if n < 12 || n > len(data)-off {
			return nil, timebase.Newf(timebase.CodeBoxTruncated, opts.TrackID, opts.BaseOffset+int64(off-4),
				"rtp packet length %d exceeds remaining %d", n, len(data)-off)
		}
		raw := data[off : off+n]
		var pkt rtp.Packet
		if err := pkt.Unmarshal(raw); err != nil {
			return nil, timebase.Fail(timebase.CodeCorruptField, opts.TrackID, opts.BaseOffset+int64(off)).
				Wrap(err, "decode rtp packet")
		}
		if consumed := rtpPacketSize(&pkt, raw); consumed != n {
			return nil, timebase.Newf(timebase.CodeCorruptField, opts.TrackID, opts.BaseOffset+int64(off),
				"rtp packet declares %d bytes but header/payload span %d", n, consumed)
		}
		if seen[pkt.SequenceNumber] {
			return nil, timebase.Newf(timebase.CodeCorruptField, opts.TrackID, opts.BaseOffset+int64(off),
				"duplicate rtp sequence number %d", pkt.SequenceNumber)
		}
		seen[pkt.SequenceNumber] = true
		sess.SSRC = pkt.SSRC
		sess.Packets = append(sess.Packets, RTPPacket{
			Header:  pkt.Header,
			Payload: append([]byte(nil), pkt.Payload...),
			Offset:  opts.BaseOffset + int64(off),
		})
		off += n
	}
	if len(sess.Packets) == 0 {
		return nil, timebase.Newf(timebase.CodeCorruptField, opts.TrackID, opts.BaseOffset,
			"rtp capture contains no packets")
	}
	// Stable insertion sort by sequence number so wrap-around is not guessed.
	for i := 1; i < len(sess.Packets); i++ {
		for j := i; j > 0 && sess.Packets[j].Header.SequenceNumber < sess.Packets[j-1].Header.SequenceNumber; j-- {
			sess.Packets[j], sess.Packets[j-1] = sess.Packets[j-1], sess.Packets[j]
		}
	}
	first := sess.Packets[0].Header.Timestamp
	prev := first
	for i, p := range sess.Packets {
		if i > 0 && p.Header.Timestamp < prev {
			return nil, timebase.Newf(timebase.CodeTimeRegression, opts.TrackID, p.Offset,
				"rtp timestamp %d at seq %d precedes %d", p.Header.Timestamp, p.Header.SequenceNumber, prev)
		}
		prev = p.Header.Timestamp
		rel := int64(p.Header.Timestamp - first)
		ns, e := base.ToNanos(rel, opts.TrackID, p.Offset)
		if e != nil {
			return nil, e
		}
		startTS := ns + opts.AnchorNS
		if p.Header.Marker {
			ev := Event{
				ID:           fmt.Sprintf("rtp-%05d", p.Header.SequenceNumber),
				Kind:         KindRTP,
				TrackID:      opts.TrackID,
				Source:       "rtp",
				StartNS:      startTS,
				EndNS:        startTS,
				Timebase:     base.String() + " (rtp)",
				Text:         fmt.Sprintf("pt=%d seq=%d ssrc=%d", p.Header.PayloadType, p.Header.SequenceNumber, p.Header.SSRC),
				SourceOffset: p.Offset,
			}
			if e := ev.Validate(opts); e != nil {
				return nil, e
			}
			sess.Events = append(sess.Events, ev)
		}
	}
	if e := ValidateOrder(sess.Events, opts); e != nil {
		return nil, e
	}
	return sess, nil
}

// rtpPacketSize computes the on-wire size of a packet. pion's Unmarshal does
// not report consumed bytes, so the header is re-walked here.
func rtpPacketSize(p *rtp.Packet, raw []byte) int {
	h := 12 + 4*len(p.CSRC)
	if p.Extension {
		if h+4 > len(raw) {
			return 0
		}
		extLen := int(raw[h+2])<<8 | int(raw[h+3])
		h += 4 + 4*extLen
	}
	size := h + len(p.Payload)
	if p.Padding {
		size += int(p.Header.PaddingSize)
	}
	return size
}

// RTPSummary returns a short human description for verify output.
func (s *RTPSession) RTPSummary() string {
	return fmt.Sprintf("ssrc=%d packets=%d events=%d clock=%d", s.SSRC, len(s.Packets), len(s.Events), s.ClockRate)
}
