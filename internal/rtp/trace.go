// Package rtp trace parses weak-network RTP trajectories and classifies anomalies.
package rtp

import (
	"fmt"
	"sort"

	pionrtp "github.com/pion/rtp"
)

// SegmentResolver maps an RTP media timestamp to a deterministic segment ID.
type SegmentResolver func(timestamp uint32) string

// Event is one classified arrival or local failure location.
type Event struct {
	ArrivalIndex int    `json:"arrival_index"`
	Sequence     uint16 `json:"sequence"`
	ExtSequence  uint64 `json:"extended_sequence"`
	Timestamp    uint32 `json:"timestamp"`
	SSRC         uint32 `json:"ssrc"`
	Kind         string `json:"kind"`
	SegmentID    string `json:"segment_id,omitempty"`
	Message      string `json:"message,omitempty"`
}

// Counts contains the required weak-network counters.
type Counts struct {
	Observed      int `json:"observed"`
	Accepted      int `json:"accepted"`
	Lost          int `json:"lost"`
	Duplicate     int `json:"duplicate"`
	OutOfOrder    int `json:"out_of_order"`
	Late          int `json:"late"`
	SequenceWraps int `json:"sequence_wraps"`
}

// Report is the deterministic trace analysis result.
type Report struct {
	SSRC         uint32   `json:"ssrc"`
	Window       uint64   `json:"reorder_window"`
	Counts       Counts   `json:"counts"`
	Events       []Event  `json:"events"`
	LateFailures []Event  `json:"late_failures"`
	Ordered      []uint64 `json:"ordered_ext_sequences"`
}

// ParseBytes unmarshals one RTP packet using pion/rtp.
func ParseBytes(b []byte) (pionrtp.Packet, error) {
	var p pionrtp.Packet
	if err := p.Unmarshal(b); err != nil {
		return pionrtp.Packet{}, fmt.Errorf("parse rtp packet: %w", err)
	}
	return p, nil
}

// ParseTrace unmarshals every packet then analyzes arrival order.
func ParseTrace(blobs [][]byte, window uint64, resolve SegmentResolver) (*Report, error) {
	packets := make([]pionrtp.Packet, 0, len(blobs))
	for i, b := range blobs {
		p, err := ParseBytes(b)
		if err != nil {
			return nil, fmt.Errorf("packet %d: %w", i, err)
		}
		packets = append(packets, p)
	}
	return Analyze(packets, window, resolve), nil
}

type accepted struct {
	pkt pionrtp.Packet
	ext uint64
}

// Analyze classifies loss, duplicates, reordering and window-late arrivals.
func Analyze(packets []pionrtp.Packet, window uint64, resolve SegmentResolver) *Report {
	if window == 0 {
		window = 64
	}
	r := &Report{Window: window, Events: []Event{}, LateFailures: []Event{}, Ordered: []uint64{}}
	if len(packets) > 0 {
		r.SSRC = packets[0].SSRC
	}
	seen := make(map[uint64]bool)
	missing := make(map[uint64]bool)
	acceptedPackets := make([]accepted, 0, len(packets))
	var maxExt uint64
	have := false
	lastRaw := uint16(0)
	wrapCount := 0

	for i, p := range packets {
		ext := extendSequence(p.SequenceNumber, &maxExt, &have, &lastRaw, &wrapCount)
		seg := ""
		if resolve != nil {
			seg = resolve(p.Timestamp)
		}
		ev := Event{
			ArrivalIndex: i, Sequence: p.SequenceNumber, ExtSequence: ext,
			Timestamp: p.Timestamp, SSRC: p.SSRC, SegmentID: seg,
		}
		r.Counts.Observed++

		if seen[ext] {
			r.Counts.Duplicate++
			ev.Kind = "duplicate"
			r.Events = append(r.Events, ev)
			continue
		}
		seen[ext] = true

		if !have || ext > maxExt {
			if have && ext > maxExt+1 {
				for seq := maxExt + 1; seq < ext; seq++ {
					if !seen[seq] {
						missing[seq] = true
					}
				}
			}
			if !have {
				have = true
			}
			maxExt = ext
			ev.Kind = "in_order"
			acceptedPackets = append(acceptedPackets, accepted{pkt: p, ext: ext})
			r.Counts.Accepted++
			r.Events = append(r.Events, ev)
			continue
		}

		// ext <= maxExt: fill a previously observed gap.
		delete(missing, ext)
		distance := maxExt - ext
		if distance == 0 {
			// Defensive: seen map normally handles this before this point.
			r.Counts.Duplicate++
			ev.Kind = "duplicate"
			r.Events = append(r.Events, ev)
			continue
		}
		if distance <= window {
			r.Counts.OutOfOrder++
			ev.Kind = "reordered"
			ev.Message = fmt.Sprintf("returned within %d-sequence window", window)
			r.Events = append(r.Events, ev)
			acceptedPackets = append(acceptedPackets, accepted{pkt: p, ext: ext})
			r.Counts.Accepted++
		} else {
			r.Counts.Late++
			ev.Kind = "late_outside_window"
			ev.Message = fmt.Sprintf("sequence is %d behind max %d; local failure only", distance, maxExt)
			r.Events = append(r.Events, ev)
			r.LateFailures = append(r.LateFailures, ev)
		}
	}

	r.Counts.Lost = len(missing)
	r.Counts.SequenceWraps = wrapCount
	sort.SliceStable(acceptedPackets, func(i, j int) bool { return acceptedPackets[i].ext < acceptedPackets[j].ext })
	r.Ordered = make([]uint64, 0, len(acceptedPackets))
	for _, a := range acceptedPackets {
		r.Ordered = append(r.Ordered, a.ext)
	}
	return r
}

func extendSequence(seq uint16, maxExt *uint64, have *bool, lastRaw *uint16, wraps *int) uint64 {
	if !*have {
		*lastRaw = seq
		return uint64(seq)
	}
	candidate := (*maxExt &^ 0xffff) + uint64(seq)
	delta := int64(candidate) - int64(*maxExt)
	if delta > 32768 {
		candidate -= 1 << 16
	} else if delta < -32768 {
		candidate += 1 << 16
		if seq < *lastRaw {
			*wraps++
		}
	}
	*lastRaw = seq
	return candidate
}
