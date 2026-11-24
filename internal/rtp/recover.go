package rtp

import (
	"github.com/local/medialab/internal/model"
)

// FECGroup bundles one repair packet with the sequence numbers of the
// media packets it protects. The fixture emits groups of a fixed size;
// the group is identified by the repair packet's timestamp.
type FECGroup struct {
	MediaSeqs []uint16
	Parity    []byte
	// Received media payloads keyed by sequence number.
	received map[uint16][]byte
}

// RecoveryResult summarizes one repair pass.
type RecoveryResult struct {
	Recovered   int
	Unrecovered int
	RepairUsed  int
}

// Recoverer performs XOR parity repair. A group reconstructs at most one
// missing media packet: XOR of parity with all surviving payloads. Groups
// missing two or more packets are reported unrecoverable, never guessed.
type Recoverer struct {
	groups map[uint32]*FECGroup // keyed by group base RTP timestamp
}

// NewRecoverer builds an empty recoverer.
func NewRecoverer() *Recoverer {
	return &Recoverer{groups: map[uint32]*FECGroup{}}
}

// AddRepair registers a repair packet and the media sequences it covers.
// Media observed earlier is preserved.
func (r *Recoverer) AddRepair(baseTS uint32, mediaSeqs []uint16, parity []byte) {
	g, ok := r.groups[baseTS]
	if !ok {
		g = &FECGroup{received: map[uint16][]byte{}}
		r.groups[baseTS] = g
	}
	g.MediaSeqs = append([]uint16(nil), mediaSeqs...)
	g.Parity = append([]byte(nil), parity...)
}

// ObserveMedia records a received media payload for later reconstruction.
// Groups are created on demand so media may be observed before the repair
// packet arrives.
func (r *Recoverer) ObserveMedia(baseTS uint32, seq uint16, payload []byte) {
	g, ok := r.groups[baseTS]
	if !ok {
		g = &FECGroup{received: map[uint16][]byte{}}
		r.groups[baseTS] = g
	}
	g.received[seq] = append([]byte(nil), payload...)
}

// Recover runs reconstruction across every registered group and returns
// payloads keyed by the recovered media sequence number. Unrecoverable
// groups increment the result counters and, when a book is supplied,
// produce located error records.
func (r *Recoverer) Recover(book *model.ErrorBook, missing []uint16, groupOf func(seq uint16) (uint32, bool)) (map[uint16][]byte, RecoveryResult) {
	out := map[uint16][]byte{}
	res := RecoveryResult{}

	missingByGroup := map[uint32][]uint16{}
	for _, seq := range missing {
		baseTS, ok := groupOf(seq)
		if !ok {
			res.Unrecovered++
			continue
		}
		missingByGroup[baseTS] = append(missingByGroup[baseTS], seq)
	}

	for baseTS, missingSeqs := range missingByGroup {
		g, ok := r.groups[baseTS]
		if !ok {
			res.Unrecovered += len(missingSeqs)
			continue
		}
		res.RepairUsed++
		if len(missingSeqs) != 1 {
			// XOR parity can only fill exactly one hole.
			res.Unrecovered += len(missingSeqs)
			if book != nil {
				book.Add(model.ErrorRecord{
					Code: model.CodeUnrecoverable, Track: 1,
					Detail: "FEC group has multiple losses; parity insufficient",
				})
			}
			continue
		}
		miss := missingSeqs[0]
		rebuilt := append([]byte(nil), g.Parity...)
		for _, seq := range g.MediaSeqs {
			if seq == miss {
				continue
			}
			payload, ok := g.received[seq]
			if !ok {
				rebuilt = nil
				break
			}
			rebuilt = xorInto(rebuilt, payload)
		}
		if rebuilt == nil {
			res.Unrecovered++
			continue
		}
		out[miss] = rebuilt
		res.Recovered++
	}
	return out, res
}

// RecoveryRate returns recovered/(recovered+unrecovered), defined as 1
// when there was nothing to recover.
func RecoveryRate(res RecoveryResult) float64 {
	den := res.Recovered + res.Unrecovered
	if den == 0 {
		return 1
	}
	return float64(res.Recovered) / float64(den)
}

func xorInto(dst, src []byte) []byte {
	if len(dst) < len(src) {
		grown := make([]byte, len(src))
		copy(grown, dst)
		dst = grown
	}
	for i := range src {
		dst[i] ^= src[i]
	}
	return dst
}
