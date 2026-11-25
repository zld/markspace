// Package fingerprint identifies repeated media content from normalized
// content hashes. RTP transport fields (sequence number, timestamp, SSRC)
// are deliberately excluded: identical payloads carried by different packets
// are still the same content, whether retransmitted or replayed.
package fingerprint

import (
	"hash/fnv"
	"strconv"

	"rtpreplay/ingest"
	"rtpreplay/pipeline"
)

// DefaultWindow is the number of consecutive packets whose combined payload
// forms one fingerprint unit.
const DefaultWindow = 1

// Duplicate links a repeated content window to its first occurrence.
type Duplicate struct {
	// First occurrence (retained position).
	FirstStartRecord int `json:"first_start_record"`
	FirstEndRecord   int `json:"first_end_record"`
	// Later matching position.
	MatchStartRecord int    `json:"match_start_record"`
	MatchEndRecord   int    `json:"match_end_record"`
	WindowPackets    int    `json:"window_packets"`
	Fingerprint      string `json:"fingerprint"`
}

// Fingerprint hashes normalized content bytes with FNV-1a 64-bit.
func Fingerprint(payload []byte) uint64 {
	h := fnv.New64a()
	h.Write(payload)
	return h.Sum64()
}

func fpString(v uint64) string { return strconv.FormatUint(v, 16) }

// windowHash combines the payload fingerprints of a consecutive packet run.
func windowHash(pkts []*ingest.Packet, start, window int) (uint64, bool) {
	h := fnv.New64a()
	for i := 0; i < window; i++ {
		p := pkts[start+i]
		if p.Clock.Isolated || p.RTP == nil || len(p.RTP.Payload) == 0 {
			return 0, false // evidence gaps cannot be fingerprinted
		}
		var b [8]byte
		v := Fingerprint(p.RTP.Payload)
		for k := 7; k >= 0; k-- {
			b[k] = byte(v)
			v >>= 8
		}
		h.Write(b[:])
	}
	return h.Sum64(), true
}

// Dedupe scans one representation for repeated content windows. The first
// occurrence of every window hash is retained; every later occurrence yields
// a Duplicate pointing back at it. Adjacent matching windows are merged into
// one fragment so a long repeated passage is reported once.
func Dedupe(rep *pipeline.Representation, window int) []Duplicate {
	if window <= 0 {
		window = DefaultWindow
	}
	pkts := rep.Stream.Packets
	if len(pkts) < window {
		return nil
	}
	type occurrence struct{ start, end int }
	first := map[uint64]occurrence{}
	var raw []Duplicate
	for start := 0; start+window <= len(pkts); start++ {
		hash, ok := windowHash(pkts, start, window)
		if !ok {
			continue
		}
		cur := occurrence{start: start, end: start + window - 1}
		if prev, seen := first[hash]; seen {
			raw = append(raw, Duplicate{
				FirstStartRecord: pkts[prev.start].RecordIndex,
				FirstEndRecord:   pkts[prev.end].RecordIndex,
				MatchStartRecord: pkts[cur.start].RecordIndex,
				MatchEndRecord:   pkts[cur.end].RecordIndex,
				WindowPackets:    window,
				Fingerprint:      fpString(hash),
			})
		} else {
			first[hash] = cur
		}
	}
	return mergeAdjacent(raw)
}

// mergeAdjacent fuses duplicate windows whose match ranges are consecutive
// and share the same first occurrence and fingerprint.
func mergeAdjacent(in []Duplicate) []Duplicate {
	var out []Duplicate
	for _, d := range in {
		if len(out) > 0 {
			last := &out[len(out)-1]
			connected := last.Fingerprint == d.Fingerprint &&
				last.FirstStartRecord == d.FirstStartRecord &&
				last.FirstEndRecord == d.FirstEndRecord &&
				d.MatchStartRecord == last.MatchEndRecord+1
			if connected {
				last.MatchEndRecord = d.MatchEndRecord
				last.WindowPackets += d.WindowPackets
				continue
			}
		}
		out = append(out, d)
	}
	return out
}
