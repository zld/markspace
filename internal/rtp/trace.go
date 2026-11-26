// Package rtp reorders and recovers RTP traces using pion/rtp packet parsing.
package rtp

import (
	"sort"

	"github.com/pion/rtp"
)

// Arrival is one observed RTP packet. Retransmission metadata is carried
// outside the standard RTP header because original sequence mapping depends on
// the chosen RTX/retransmission convention.
type Arrival struct {
	Packet           rtp.Packet `json:"packet"`
	Retransmission   bool       `json:"retransmission,omitempty"`
	OriginalSequence uint16     `json:"original_sequence,omitempty"`
}

// ParsePacket parses a raw RTP packet with pion/rtp.
func ParsePacket(wire []byte) (rtp.Packet, error) {
	var p rtp.Packet
	if err := p.Unmarshal(wire); err != nil {
		return rtp.Packet{}, err
	}
	return p, nil
}

// NewArrival builds a packet, round-trips it through pion marshal/parse, and
// returns an arrival whose header values therefore came from the RTP parser.
func NewArrival(seq uint16, ts uint32, ssrc uint32, marker bool, payload []byte, retrans bool, origSeq uint16) (Arrival, error) {
	src := rtp.Packet{Header: rtp.Header{
		Version: 2, Marker: marker, SequenceNumber: seq, Timestamp: ts, SSRC: ssrc,
	}, Payload: payload}
	wire, err := src.Marshal()
	if err != nil {
		return Arrival{}, err
	}
	parsed, err := ParsePacket(wire)
	if err != nil {
		return Arrival{}, err
	}
	return Arrival{Packet: parsed, Retransmission: retrans, OriginalSequence: origSeq}, nil
}

// Config bounds the reorder/lateness window.
type Config struct {
	Window int `json:"window"`
}

// Report contains recovered packets and audit counters.
type Report struct {
	Ordered         []rtp.Packet `json:"-"`
	Sequences       []uint16     `json:"sequences"`
	Timestamps      []uint32     `json:"timestamps"`
	ObservedUnique  int          `json:"observed_unique"`
	Duplicates      int          `json:"duplicates"`
	Reordered       int          `json:"reordered"`
	LateDropped     int          `json:"late_dropped"`
	Retransmissions int          `json:"retransmissions"`
	Recovered       int          `json:"recovered"`
	Losses          int          `json:"losses"`
	Unrecovered     []uint16     `json:"unrecovered"`
	RecoveryPct     float64      `json:"loss_recovery_pct"`
}

type pktInfo struct {
	ext int64
	p   rtp.Packet
}

// RecoverTrace deduplicates, window-filters, reorders, and applies matching
// retransmissions. Duplicate packets never increment media/recovery counts.
func RecoverTrace(input []Arrival, cfg Config) *Report {
	window := cfg.Window
	if window <= 0 {
		window = 64
	}
	r := &Report{}
	seen := map[uint16]bool{}
	accepted := map[uint16]pktInfo{}
	recovered := map[uint16]bool{}
	var maxExt int64
	haveMax := false
	var minSeq, maxSeq uint16
	haveRange := false

	for _, a := range input {
		seq := a.Packet.SequenceNumber
		if a.Retransmission {
			orig := a.OriginalSequence
			if orig == 0 {
				orig = seq
			}
			r.Retransmissions++
			if seen[orig] || recovered[orig] {
				r.Duplicates++
				continue
			}
			recovered[orig] = true
			seen[orig] = true
			r.Recovered++
			// Retransmitted headers may use their own seq; retain timestamp.
			accepted[orig] = pktInfo{ext: extendSequence(orig, maxExt), p: a.Packet}
			continue
		}

		if seen[seq] {
			r.Duplicates++
			continue
		}
		ext := extendSequence(seq, maxExt)
		if haveMax && ext < maxExt-int64(window) {
			r.LateDropped++
			continue
		}
		if haveMax && ext < maxExt {
			r.Reordered++
		}
		seen[seq] = true
		accepted[seq] = pktInfo{ext: ext, p: a.Packet}
		if !haveMax || ext > maxExt {
			maxExt = ext
		}
		if !haveRange {
			minSeq, maxSeq, haveRange = seq, seq, true
		} else {
			if seqLess(seq, minSeq) {
				minSeq = seq
			}
			if seqLess(maxSeq, seq) {
				maxSeq = seq
			}
		}
	}

	if haveRange {
		start := uint32(minSeq)
		end := uint32(maxSeq)
		// Handle a small wrapping fixture range.
		if seqLess(maxSeq, minSeq) {
			end += 1 << 16
		}
		for v := start; v <= end; v++ {
			s := uint16(v)
			if !seen[s] {
				r.Unrecovered = append(r.Unrecovered, s)
			}
		}
		r.Losses = r.Recovered + len(r.Unrecovered)
	}
	if r.Losses > 0 {
		r.RecoveryPct = float64(r.Recovered) * 100 / float64(r.Losses)
	}

	infos := make([]pktInfo, 0, len(accepted))
	for _, info := range accepted {
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ext < infos[j].ext })
	for _, info := range infos {
		r.Ordered = append(r.Ordered, info.p)
		r.Sequences = append(r.Sequences, info.p.SequenceNumber)
		r.Timestamps = append(r.Timestamps, info.p.Timestamp)
	}
	r.ObservedUnique = len(accepted)
	return r
}

func seqLess(a, b uint16) bool {
	const half = uint32(1) << 15
	d := uint32(a) - uint32(b)
	return d > half
}

func extendSequence(seq uint16, prev int64) int64 {
	if prev == 0 {
		return int64(seq)
	}
	prevLow := uint16(prev % 65536)
	delta := int32(seq) - int32(prevLow)
	if delta > 32767 {
		delta -= 65536
	} else if delta < -32768 {
		delta += 65536
	}
	return prev + int64(delta)
}
