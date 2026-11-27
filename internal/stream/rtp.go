package stream

import (
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/pion/rtp"

	"archivecheck/internal/asset"
	"archivecheck/internal/fault"
	"archivecheck/internal/timebase"
)

// RTPStream summarises an RTP packet capture.
type RTPStream struct {
	Source    string         `json:"source"`
	Packets   int            `json:"packets"`
	SSRC      uint32         `json:"ssrc"`
	Payload   uint8          `json:"payloadType"`
	ClockRate uint32         `json:"clockRate"`
	FirstSeq  uint16         `json:"firstSequence"`
	LastSeq   uint16         `json:"lastSequence"`
	Lost      int            `json:"lost"`
	Gaps      []SequenceGap  `json:"gaps,omitempty"`
	Damaged   []fault.Span   `json:"damaged,omitempty"`
	FirstTime timebase.Nanos `json:"firstTimestampNanos"`
	LastTime  timebase.Nanos `json:"lastTimestampNanos"`
	Duration  timebase.Nanos `json:"durationNanos"`
}

// SequenceGap records a run of missing sequence numbers. Offset is the byte
// position where the gap was detected: the header of the first packet that
// followed the missing run.
type SequenceGap struct {
	Expected uint16 `json:"expected"`
	Got      uint16 `json:"got"`
	Missing  int    `json:"missing"`
	Offset   int64  `json:"offset"`
	Packet   int    `json:"packet"`
}

// DefaultRTPClockRate is the RTP clock rate assumed when a capture carries no
// timing hint; 90 kHz is the MPEG-TS convention.
const DefaultRTPClockRate = 90000

// AnalyzeRTP decodes a length-prefixed capture: each record is a 4-byte
// big-endian length followed by that many RTP packet bytes. Truncated records
// and undecodable packets are reported as damaged spans; sequence gaps are
// reported at the offset of the first packet received after the gap.
func AnalyzeRTP(src *asset.Source) *RTPStream {
	s := &RTPStream{Source: src.Name, ClockRate: DefaultRTPClockRate}
	data := src.Bytes()
	add := func(start, end int64, kind, detail string) {
		if len(s.Damaged) >= maxReportedSpans {
			return
		}
		if end <= start {
			end = start + 1
		}
		if end > src.Size {
			end = src.Size
		}
		s.Damaged = append(s.Damaged, fault.Span{Start: start, End: end, Kind: kind, Detail: detail})
	}
	var last uint16
	var lastTS uint32
	for off := 0; off < len(data); {
		start := off
		if len(data)-off < 4 {
			add(int64(start), int64(len(data)), fault.KindTruncated,
				fmt.Sprintf("record length prefix is truncated: %d bytes remain", len(data)-off))
			break
		}
		n := int(binary.BigEndian.Uint32(data[off:]))
		off += 4
		if n <= 0 {
			add(int64(start), int64(off), fault.KindInvalid, fmt.Sprintf("record declares %d bytes", n))
			continue
		}
		if n > len(data)-off {
			add(int64(start), int64(len(data)), fault.KindTruncated,
				fmt.Sprintf("record declares %d bytes but only %d remain", n, len(data)-off))
			break
		}
		payload := data[off : off+n]
		off += n
		var pkt rtp.Packet
		if err := pkt.Unmarshal(payload); err != nil {
			add(int64(start+4), int64(start+4+n), fault.KindCorrupt, "undecodable RTP packet: "+err.Error())
			continue
		}
		s.Packets++
		if s.Packets == 1 {
			s.SSRC = pkt.SSRC
			s.Payload = pkt.PayloadType
			s.FirstSeq = pkt.SequenceNumber
			s.FirstTime = rtpToNanos(pkt.Timestamp, s.ClockRate)
		} else if pkt.SSRC != s.SSRC {
			add(int64(start+4), int64(start+4+n), fault.KindConflict,
				fmt.Sprintf("packet %d switches SSRC from %d to %d", s.Packets, s.SSRC, pkt.SSRC))
		}
		if s.Packets > 1 {
			delta := int(pkt.SequenceNumber) - int(last) - 1
			if delta < -32768 {
				delta += 65536
			} else if delta > 32767 {
				delta -= 65536
			}
			if delta > 0 {
				s.Lost += delta
				s.Gaps = append(s.Gaps, SequenceGap{
					Expected: last + 1,
					Got:      pkt.SequenceNumber,
					Missing:  delta,
					Offset:   int64(start),
					Packet:   s.Packets,
				})
				add(int64(start), int64(start+4), fault.KindMissing,
					fmt.Sprintf("%d RTP packet(s) missing after sequence %d, next is %d", delta, last, pkt.SequenceNumber))
			} else if delta < 0 && delta > -32768 && pkt.SequenceNumber == last {
				add(int64(start), int64(start+4), fault.KindConflict,
					fmt.Sprintf("duplicate RTP sequence number %d", pkt.SequenceNumber))
			}
		}
		last = pkt.SequenceNumber
		lastTS = pkt.Timestamp
		s.LastSeq = pkt.SequenceNumber
		s.LastTime = rtpToNanos(lastTS, s.ClockRate)
	}
	if s.LastTime < s.FirstTime {
		s.LastTime = s.FirstTime
	}
	s.Duration = s.LastTime - s.FirstTime
	sort.SliceStable(s.Gaps, func(i, j int) bool { return s.Gaps[i].Offset < s.Gaps[j].Offset })
	return s
}

// Bad reports whether the capture has any damaged span or sequence gap.
func (s *RTPStream) Bad() bool { return len(s.Damaged) > 0 || len(s.Gaps) > 0 }

// BuildRTP encodes packets into the length-prefixed capture format, mirroring
// what AnalyzeRTP expects.
func BuildRTP(packets []*rtp.Packet) ([]byte, error) {
	var out []byte
	for i, p := range packets {
		raw, err := p.Marshal()
		if err != nil {
			return nil, fmt.Errorf("marshal packet %d: %w", i, err)
		}
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], uint32(len(raw)))
		out = append(out, prefix[:]...)
		out = append(out, raw...)
	}
	return out, nil
}

// rtpToNanos converts a 32-bit RTP timestamp at clockRate onto the shared
// nanosecond scale without unwrapping the timestamp.
func rtpToNanos(ts uint32, clockRate uint32) timebase.Nanos {
	if clockRate == 0 {
		return 0
	}
	return timebase.Nanos(int64(ts) * 1e9 / int64(clockRate))
}
