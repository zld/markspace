package analyze

import (
	"errors"
	"math"
	"sort"

	"github.com/pion/rtp"

	"mediamux/media"
)

// RTPOptions tunes transport-level validation.
type RTPOptions struct {
	// MaxPayload bounds a single packet payload; larger packets are reported
	// as malformed rather than trusted.
	MaxPayload int
	// ClockRate converts RTP timestamps to the unified timeline.
	ClockRate uint32
}

// RTPPacket is one observed packet with its validation verdict.
type RTPPacket struct {
	Index      int    `json:"index"`
	Sequence   uint16 `json:"sequence"`
	Timestamp  uint32 `json:"timestamp"`
	PayloadLen int    `json:"payload_len"`
	Marker     bool   `json:"marker"`
	SSRC       uint32 `json:"ssrc"`
	Duplicate  bool   `json:"duplicate"`
	Valid      bool   `json:"valid"`
	Reason     string `json:"reason,omitempty"`
}

// RTPGap is a missing run of sequence numbers.
type RTPGap struct {
	From     uint16 `json:"from"`
	To       uint16 `json:"to"`
	Count    int    `json:"count"`
	PacketAt int    `json:"packet_at"`
}

// RTPResult is the transport analysis of one packet stream.
type RTPResult struct {
	Packets        []RTPPacket `json:"packets,omitempty"`
	Gaps           []RTPGap    `json:"gaps,omitempty"`
	Duplicates     int         `json:"duplicates"`
	DuplicateBytes int64       `json:"duplicate_payload_bytes"`
	Malformed      int         `json:"malformed"`
	Missing        int         `json:"missing"`
	PayloadBytes   int64       `json:"payload_bytes"`
	UniquePackets  int         `json:"unique_packets"`
	FirstSeq       uint16      `json:"first_seq"`
	LastSeq        uint16      `json:"last_seq"`
	Wraps          int         `json:"wraps"`
	TimestampMS    int64       `json:"timestamp_ms"`
	MissingMS      int64       `json:"missing_ms"`
	Duration       media.Time  `json:"duration"`
	Corrupt        bool        `json:"corrupt"`
}

// AnalyzeRTP validates packet sequence numbers, timestamps and payload bounds
// using pion/rtp for parsing. Sequence wrap-around is handled modulo 65536;
// duplicate packets are counted once and never inflate the byte total.
func AnalyzeRTP(raw [][]byte, opts RTPOptions) *RTPResult {
	if opts.MaxPayload <= 0 {
		opts.MaxPayload = 1200
	}
	if opts.ClockRate == 0 {
		opts.ClockRate = 90000
	}
	res := &RTPResult{}
	seen := make(map[uint16]bool, len(raw))
	var (
		haveSeq bool
		prevSeq uint16
		firstTS uint32
		lastTS  uint32
		haveTS  bool
		ssrcSet bool
		ssrc    uint32
	)
	for i, buf := range raw {
		pkt := RTPPacket{Index: i, Valid: true}
		var p rtp.Packet
		if err := p.Unmarshal(buf); err != nil {
			pkt.Valid, pkt.Reason = false, "rtp header: "+err.Error()
			res.Malformed++
			res.Packets = append(res.Packets, pkt)
			continue
		}
		pkt.Sequence = p.SequenceNumber
		pkt.Timestamp = p.Timestamp
		pkt.Marker = p.Marker
		pkt.SSRC = p.SSRC
		pkt.PayloadLen = len(p.Payload)
		switch {
		case !ssrcSet:
			ssrc, ssrcSet = p.SSRC, true
		case p.SSRC != ssrc:
			pkt.Valid, pkt.Reason = false, "ssrc changed mid-stream"
			res.Malformed++
		}
		if len(p.Payload) == 0 {
			pkt.Valid, pkt.Reason = false, "empty payload"
			res.Malformed++
		} else if len(p.Payload) > opts.MaxPayload {
			pkt.Valid, pkt.Reason = false, "payload exceeds configured bound"
			res.Malformed++
		}
		if seen[p.SequenceNumber] {
			pkt.Duplicate = true
			res.Duplicates++
			// Duplicate payload is recorded as evidence but excluded from
			// the unique byte total used for bitrate.
			res.DuplicateBytes += int64(len(p.Payload))
			res.Packets = append(res.Packets, pkt)
			continue
		}
		seen[p.SequenceNumber] = true
		res.UniquePackets++
		if pkt.Valid {
			res.PayloadBytes += int64(len(p.Payload))
		}
		if !haveSeq {
			haveSeq, prevSeq = true, p.SequenceNumber
			res.FirstSeq = p.SequenceNumber
		} else {
			delta := int32(int16(p.SequenceNumber - prevSeq))
			switch {
			case delta == 1:
				if p.SequenceNumber < prevSeq {
					// Natural rollover 65535 -> 0 with no loss.
					res.Wraps++
				}
				prevSeq = p.SequenceNumber
			case delta > 1:
				// Forward gap, including the natural 65535 -> 0 rollover,
				// which appears as a small positive delta.
				res.Missing += int(delta) - 1
				res.Gaps = append(res.Gaps, RTPGap{
					From: prevSeq + 1, To: p.SequenceNumber - 1,
					Count: int(delta) - 1, PacketAt: i,
				})
				if p.SequenceNumber < prevSeq {
					res.Wraps++
				}
				prevSeq = p.SequenceNumber
			case delta < 0:
				// Went backwards: either reordering within the window or a
				// source restart. Record the hole without moving backwards.
				res.Gaps = append(res.Gaps, RTPGap{
					From: p.SequenceNumber, To: prevSeq,
					Count: int(-delta), PacketAt: i,
				})
				res.Missing += int(-delta)
			}
		}
		if !haveTS {
			firstTS, lastTS, haveTS = p.Timestamp, p.Timestamp, true
		} else {
			lastTS = p.Timestamp
		}
		res.Packets = append(res.Packets, pkt)
	}
	if haveSeq {
		res.LastSeq = prevSeq
	}
	if haveTS {
		deltaTS := lastTS - firstTS // unsigned: correct across rollover
		res.TimestampMS = int64(deltaTS) * 1000 / int64(opts.ClockRate)

		res.Duration = media.NewTime(int64(deltaTS), media.Timescale(opts.ClockRate))
	}
	// Convert missed packets into missed media time from the average spacing
	// of the received packets. This is exact in ticks and only rounded once,
	// at the end.
	if res.Missing > 0 && res.UniquePackets > 1 && haveTS {
		span := int64(lastTS - firstTS)
		missingTicks := span * int64(res.Missing) / int64(res.UniquePackets-1)
		res.MissingMS = missingTicks * 1000 / int64(opts.ClockRate)
	}
	res.Corrupt = res.Missing > 0 || res.Malformed > 0
	return res
}

// ErrNoPackets is returned when a transport stream is empty.
var ErrNoPackets = errors.New("analyze: no rtp packets")

// ValidateRTP fails fast on a completely empty packet stream, which is a
// caller error rather than transport damage.
func ValidateRTP(raw [][]byte, opts RTPOptions) (*RTPResult, error) {
	if len(raw) == 0 {
		return nil, media.NewError(media.CodeInput, media.PhaseTransport, ErrNoPackets.Error()).AtOffset(0)
	}
	return AnalyzeRTP(raw, opts), nil
}

// BuildRTP marshals deterministic RTP packets for a fixture: a single SSRC,
// a fixed payload pattern and incrementing sequence/timestamp.
func BuildRTP(count int, seqStart uint16, tsStart uint32, payloadLen int, dropSeq map[uint16]bool, dupSeq map[uint16]bool) ([][]byte, error) {
	if count <= 0 || payloadLen <= 0 {
		return nil, media.NewError(media.CodeInput, media.PhaseTransport, "invalid rtp fixture shape").AtOffset(0)
	}
	out := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		seq := seqStart + uint16(i)
		if dropSeq[seq] {
			continue
		}
		payload := make([]byte, payloadLen)
		for j := range payload {
			payload[j] = byte((i + j) % 251)
		}
		p := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: seq,
				Timestamp:      tsStart + uint32(i)*3000,
				SSRC:           0x0badf00d,
				Marker:         i == count-1,
			},
			Payload: payload,
		}
		buf, err := p.Marshal()
		if err != nil {
			return nil, media.NewError(media.CodeInternal, media.PhaseTransport, err.Error()).AtOffset(int64(i))
		}
		out = append(out, buf)
		if dupSeq[seq] {
			out = append(out, append([]byte(nil), buf...))
		}
	}
	return out, nil
}

// Percentile returns the nearest-rank percentile of vals, treating p in
// [0,1]. It copies rather than mutating the caller's slice.
func Percentile(vals []int64, p float64) int64 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]int64(nil), vals...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	if p <= 0 {
		return cp[0]
	}
	if p >= 1 {
		return cp[len(cp)-1]
	}
	rank := int(math.Ceil(p*float64(len(cp)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(cp) {
		rank = len(cp) - 1
	}
	return cp[rank]
}
