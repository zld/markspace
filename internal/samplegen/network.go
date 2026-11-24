package samplegen

import (
	"math/rand"

	"medpipe/internal/fault"
	medrtp "medpipe/internal/rtp"
)

// NetworkStats describes deterministic impairments injected before reordering.
type NetworkStats struct {
	SourcePackets      int `json:"source_packets"`
	ArrivalPackets     int `json:"arrival_packets"`
	DuplicateCopies    int `json:"duplicate_copies_injected"`
	ReorderedPairs     int `json:"reordered_pairs"`
	DroppedPrimary     int `json:"dropped_primary"`
	RedundantRecovered int `json:"redundant_recovered"`
	UnrecoveredLosses  int `json:"unrecovered_losses"`
}

// PacketizeTS splits TS into multi-packet RTP payloads. Chunk sizes are
// multiples of 188 so a lost chunk preserves TS packet alignment for the
// chunks that remain.
func PacketizeTS(data []byte, ssrc uint32) []medrtp.Packet {
	const chunk = 7 * 188
	seq := uint16(1000)
	var out []medrtp.Packet
	for off := 0; off < len(data); off += chunk {
		end := off + chunk
		if end > len(data) {
			end = len(data)
		}
		out = append(out, medrtp.Packet{
			Sequence:  seq,
			Timestamp: uint32(seq) * 9000,
			SSRC:      ssrc,
			Marker:    end == len(data),
			Payload:   append([]byte(nil), data[off:end]...),
		})
		seq++
	}
	return out
}

// Impair applies the requested deterministic network faults.
func Impair(packets []medrtp.Packet, seed int64, faults map[string]bool) ([]medrtp.Packet, NetworkStats) {
	r := rand.New(rand.NewSource(seed))
	stats := NetworkStats{SourcePackets: len(packets)}
	arrivals := append([]medrtp.Packet(nil), packets...)

	if faults["burst-loss"] && len(arrivals) > 6 {
		start := 2 + r.Intn(2)
		const burst = 2
		var redundant []medrtp.Packet
		kept := arrivals[:start]
		for i := start; i < start+burst && i < len(arrivals); i++ {
			redundant = append(redundant, arrivals[i])
			stats.DroppedPrimary++
		}
		kept = append(kept, arrivals[start+burst:]...)
		arrivals = kept
		// Redundant copies arrive later but inside the large demo window.
		arrivals = append(arrivals, redundant...)
		stats.RedundantRecovered += len(redundant)
	}

	if faults["reorder"] && len(arrivals) > 4 {
		at := 1 + r.Intn(2)
		if at+1 < len(arrivals) {
			arrivals[at], arrivals[at+1] = arrivals[at+1], arrivals[at]
			stats.ReorderedPairs++
		}
	}

	if faults["duplicate"] && len(arrivals) > 4 {
		// Insert duplicates immediately after selected originals. Build a new
		// slice to keep insertion indices simple and deterministic.
		dupAt := map[int]bool{1: true, 3: true, len(arrivals) - 2: true}
		var withDups []medrtp.Packet
		for i, p := range arrivals {
			withDups = append(withDups, p)
			if dupAt[i] && stats.DuplicateCopies < 3 {
				withDups = append(withDups, p)
				stats.DuplicateCopies++
			}
		}
		arrivals = withDups
	}

	stats.ArrivalPackets = len(arrivals)
	return arrivals, stats
}

// DropSequence removes every arrival carrying seq, including injected
// duplicates/redundant copies. It models an unrecoverable network loss.
func DropSequence(packets []medrtp.Packet, seq uint16) ([]medrtp.Packet, int) {
	out := make([]medrtp.Packet, 0, len(packets))
	removed := 0
	for _, p := range packets {
		if p.Sequence == seq {
			removed++
			continue
		}
		out = append(out, p)
	}
	return out, removed
}

// ReassembleRTP runs the kernel reorderer and concatenates ordered payloads.
func ReassembleRTP(packets []medrtp.Packet, window uint16, bandwidthBps int) ([]byte, medrtp.Stats, []fault.Event) {
	rr := medrtp.NewReorderer(window)
	rr.SetBandwidth(bandwidthBps, 7*188)
	var ordered []medrtp.Packet
	for _, p := range packets {
		ordered = append(ordered, rr.Push(p)...)
	}
	ordered = append(ordered, rr.Finish()...)
	var out []byte
	for _, p := range ordered {
		out = append(out, p.Payload...)
	}
	return out, rr.Stats(), rr.Events()
}
